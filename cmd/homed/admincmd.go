package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/devXpro/homie/internal/config"
	"github.com/devXpro/homie/internal/firstrun"
)

// linkJSON is the answer of the socket's /local/setup-link and /local/reset-access (ADMIN-API §6).
type linkJSON struct {
	URL             string    `json:"url"`
	ExpiresAt       time.Time `json:"expiresAt"`
	RevokedPasskeys int       `json:"revokedPasskeys"`
	RevokedSessions int       `json:"revokedSessions"`
}

// cmdAdminSetupLink prints a one-time setup link (15 minutes): it creates the owner on a new home, or adds a passkey to
// the owner. The link goes to stdout alone, so it can be piped; it is a secret until used.
func cmdAdminSetupLink(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("admin setup-link", stderr)
	if err := noArgs(fl, args); err != nil {
		return err
	}
	link, err := adminLink(*dataDir, "/local/setup-link", stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Open this link on the phone or computer that should hold the owner's passkey. It works once, "+
		"until %s:\n", link.ExpiresAt.Local().Format(time.DateTime))
	fmt.Fprintln(stdout, link.URL)
	return nil
}

// cmdAdminResetAccess removes every passkey and session (a lost or stolen device) and prints a new setup link. Person
// keys, and so ChatGPT's access, stay.
func cmdAdminResetAccess(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("admin reset-access", stderr)
	if err := noArgs(fl, args); err != nil {
		return err
	}
	link, err := adminLink(*dataDir, "/local/reset-access", stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Removed %d passkeys and %d sessions; person keys (ChatGPT) still work.\n"+
		"Open this link to add a new passkey. It works once, until %s:\n",
		link.RevokedPasskeys, link.RevokedSessions, link.ExpiresAt.Local().Format(time.DateTime))
	fmt.Fprintln(stdout, link.URL)
	return nil
}

// cmdAdminFirstRun opens the first-run page of the running homed when it is closed (it closes 30 minutes after it
// opened, or after too many wrong PINs) and prints its addresses and its PIN. This is how to get the PIN when homed
// runs under launchd or systemd, where it never prints it; the PIN goes to this terminal only.
func cmdAdminFirstRun(args []string, stdout, stderr io.Writer) error {
	fl, dataDir := newFlags("admin firstrun", stderr)
	if err := noArgs(fl, args); err != nil {
		return err
	}
	dir, err := config.ResolveDataDir(*dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, false, stderr)
	if err != nil {
		return err
	}
	defer c.Close()
	var page struct {
		URLs     []string  `json:"urls"`
		PIN      string    `json:"pin"`
		PINUntil time.Time `json:"pinUntil"`
		ClosesAt time.Time `json:"closesAt"`
	}
	if err := c.do(ctx, "POST", "/local/firstrun", struct{}{}, &page); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "The first-run page is open until %s on:\n", page.ClosesAt.Local().Format("15:04"))
	for _, u := range page.URLs {
		fmt.Fprintf(stdout, "  %s\n", u)
	}
	fmt.Fprintf(stdout, "PIN: %s (it changes at %s and after every setup link; run this again for the current one)\n",
		firstrun.FormatPIN(page.PIN), page.PINUntil.Local().Format("15:04"))
	return nil
}

func adminLink(dataDir, path string, stderr io.Writer) (linkJSON, error) {
	dir, err := config.ResolveDataDir(dataDir)
	if err != nil {
		return linkJSON{}, err
	}
	ctx := context.Background()
	c, err := connect(ctx, dir, true, stderr)
	if err != nil {
		return linkJSON{}, err
	}
	defer c.Close()
	var link linkJSON
	err = c.do(ctx, "POST", path, struct{}{}, &link)
	return link, err
}
