package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/devXpro/homie/internal/config"
)

// minRevokePrefix keeps `key revoke` from matching by accident when only the start of a key id is given.
const minRevokePrefix = 6

// keyJSON is Key and personJSON is Person (ADMIN-API §2), as far as the CLI shows them.
type keyJSON struct {
	ID         string     `json:"id"`
	PersonID   string     `json:"personId"`
	Label      string     `json:"label"`
	Hint       string     `json:"hint"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

type personJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Plugins []string `json:"plugins"`
}

// cmdKeyNew issues a person key. The key itself is printed once to stdout (and nothing else is), or with --keychain
// saved to the macOS Keychain without ever being shown; homed keeps only its hash.
func cmdKeyNew(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("key new", stderr)
	personID := fl.String("person", "", "person id (default: the owner)")
	label := fl.String("label", "ChatGPT Site", "what the key is for")
	keychain := fl.String("keychain", "", "macOS only: save the key to this Keychain item (account = person id) instead of printing it")
	if err := noArgs(fl, args); err != nil {
		return err
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	person, err := findPerson(ctx, c, *personID)
	if err != nil {
		return err
	}
	var res struct {
		Key    keyJSON `json:"key"`
		Secret string  `json:"secret"`
	}
	if err := c.do(ctx, "POST", "/people/"+url.PathEscape(person.ID)+"/keys", map[string]string{"label": *label}, &res); err != nil {
		return err
	}
	if *keychain != "" {
		if err := keychainStore(*keychain, person.ID, res.Secret); err != nil {
			// Nobody holds the key now: take it back.
			if derr := c.do(ctx, "DELETE", "/keys/"+url.PathEscape(res.Key.ID), nil, nil); derr != nil {
				fmt.Fprintf(stderr, "Warning: revoke the unused key %s by hand: %v\n", res.Key.ID, derr)
			}
			return err
		}
		fmt.Fprintf(stdout, "New key %s for %s saved to Keychain item %q (account %s).\n", res.Key.ID, person.Name, *keychain, person.ID)
	} else {
		fmt.Fprintf(stderr, "New key %s for %s. It is shown only this once; homed keeps only its hash:\n", res.Key.ID, person.Name)
		fmt.Fprintln(stdout, res.Secret)
	}
	if c.offline {
		fmt.Fprintln(stderr, "homed is not running; the key works once it starts.")
	}
	return nil
}

// findPerson returns the person with the id, or the owner when id is empty.
func findPerson(ctx context.Context, c *apiClient, id string) (personJSON, error) {
	var people struct {
		Items []personJSON `json:"items"`
	}
	if err := c.do(ctx, "GET", "/people", nil, &people); err != nil {
		return personJSON{}, err
	}
	for _, p := range people.Items {
		if p.ID == id || id == "" && p.Role == "owner" {
			return p, nil
		}
	}
	if id == "" {
		return personJSON{}, errors.New("the home has no owner yet: finish the setup first (homed admin setup-link)")
	}
	return personJSON{}, fmt.Errorf("no person %q (see homed key list); people come from the setup link and invites", id)
}

// cmdKeyList prints every key by its id; keys themselves are never stored.
func cmdKeyList(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("key list", stderr)
	if err := noArgs(fl, args); err != nil {
		return err
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	var people struct {
		Items []personJSON `json:"items"`
	}
	if err := c.do(ctx, "GET", "/people", nil, &people); err != nil {
		return err
	}
	list, err := keyList(ctx, c)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No keys yet; make one with: homed key new")
		return nil
	}
	names := map[string]string{}
	for _, p := range people.Items {
		names[p.ID] = p.Name
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY ID\tPERSON\tLABEL\tSTARTS WITH\tCREATED\tLAST USED")
	for _, k := range list {
		hint, used := "-", "-"
		if k.Hint != "" {
			hint = k.Hint + "…"
		}
		if k.LastUsedAt != nil {
			used = k.LastUsedAt.Local().Format(time.DateTime)
		}
		fmt.Fprintf(tw, "%s\t%s (%s)\t%s\t%s\t%s\t%s\n", k.ID, names[k.PersonID], k.PersonID, k.Label, hint,
			k.CreatedAt.Local().Format(time.DateTime), used)
	}
	return tw.Flush()
}

func keyList(ctx context.Context, c *apiClient) ([]keyJSON, error) {
	var keys struct {
		Items []keyJSON `json:"items"`
	}
	err := c.do(ctx, "GET", "/keys", nil, &keys)
	return keys.Items, err
}

// cmdKeyRevoke revokes one key, named by its id or a unique start of it.
func cmdKeyRevoke(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("key revoke", stderr)
	pos, err := parseFlags(fl, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: homed key revoke <key id> (see homed key list)")
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	list, err := keyList(ctx, c)
	if err != nil {
		return err
	}
	k, err := matchKey(list, pos[0])
	if err != nil {
		return err
	}
	if err := c.do(ctx, "DELETE", "/keys/"+url.PathEscape(k.ID), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Revoked key %s (%s of %s).\n", k.ID, k.Label, k.PersonID)
	return nil
}

// matchKey finds the key whose id is ref, or the one key whose id starts with ref (at least 6 characters).
func matchKey(list []keyJSON, ref string) (keyJSON, error) {
	var found []keyJSON
	for _, k := range list {
		if k.ID == ref {
			return k, nil
		}
		if strings.HasPrefix(k.ID, ref) {
			found = append(found, k)
		}
	}
	switch {
	case len(ref) < minRevokePrefix:
		return keyJSON{}, fmt.Errorf("no key %q; give the whole key id or at least %d characters of it (see homed key list)", ref, minRevokePrefix)
	case len(found) == 0:
		return keyJSON{}, fmt.Errorf("no key id starts with %q (see homed key list)", ref)
	case len(found) > 1:
		return keyJSON{}, fmt.Errorf("%d key ids start with %q; give more characters", len(found), ref)
	}
	return found[0], nil
}
