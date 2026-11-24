package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func phase1Sample() Phase1Import {
	return Phase1Import{
		HomeName: "Мой шалаш",
		People: []Phase1Person{
			{ID: "roman", Name: "Roman", Plugins: []string{"*"}, Keys: []Phase1Key{
				{SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Label: "ChatGPT Site", Created: epoch.Add(-48 * time.Hour)},
				{SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			}},
			{ID: "tanya", Name: "Tanya", Plugins: []string{"tv"}},
		},
		Plugins: []Phase1Plugin{{ID: "tv", Command: []string{"/opt/homie-tv", "serve"}}},
	}
}

func TestImportPhase1(t *testing.T) {
	ctx := context.Background()
	db, c := newTestDB(t)

	res, err := db.ImportPhase1(ctx, phase1Sample())
	if err != nil {
		t.Fatal(err)
	}
	if want := (ImportResult{Done: true, People: 2, Keys: 2, Plugins: 1, HomeName: true}); res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}

	roman, _ := db.People().Get(ctx, "roman")
	tanya, _ := db.People().Get(ctx, "tanya")
	if roman.Role != RoleOwner || tanya.Role != RoleMember {
		t.Errorf("roles = %s, %s; want owner, member", roman.Role, tanya.Role)
	}
	if len(roman.UserHandle) != 32 || string(roman.UserHandle) == string(tanya.UserHandle) {
		t.Error("user handles are not distinct 32-byte values")
	}
	if fmt.Sprint(tanya.Plugins) != "[tv]" {
		t.Errorf("tanya plugins = %v", tanya.Plugins)
	}

	ks, _ := db.Keys().ListByPerson(ctx, "roman")
	if len(ks) != 2 {
		t.Fatalf("keys = %+v", ks)
	}
	byHash := map[string]Key{ks[0].SHA256: ks[0], ks[1].SHA256: ks[1]}
	site := byHash["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]
	if site.Label != "ChatGPT Site" || !site.CreatedAt.Equal(epoch.Add(-48*time.Hour)) || site.Hint != "" || site.ID == "" {
		t.Errorf("imported key = %+v", site)
	}
	if k := byHash["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]; !k.CreatedAt.Equal(c.now()) {
		t.Errorf("key without a date: CreatedAt = %v, want now", k.CreatedAt)
	}

	p, err := db.Plugins().Get(ctx, "tv")
	if err != nil || p.Source != PluginLocal || !p.Enabled || fmt.Sprint(p.Command) != "[/opt/homie-tv serve]" || p.Version != "" {
		t.Errorf("plugin = %+v, %v", p, err)
	}
	if s, _ := db.Settings().Get(ctx); s.HomeName != "Мой шалаш" || s.Language != "ru" {
		t.Errorf("settings = %+v", s)
	}

	// The second call, even with different content, does nothing.
	again := phase1Sample()
	again.People = append(again.People, Phase1Person{ID: "kid", Name: "Kid", Plugins: []string{}})
	res, err = db.ImportPhase1(ctx, again)
	if err != nil || res != (ImportResult{}) {
		t.Errorf("second import = %+v, %v; want zero", res, err)
	}
	if _, err := db.People().Get(ctx, "kid"); !errors.Is(err, ErrNotFound) {
		t.Error("second import added a person")
	}
}

func TestImportPhase1Empty(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	res, err := db.ImportPhase1(ctx, Phase1Import{})
	if err != nil || res != (ImportResult{}) {
		t.Fatalf("empty import = %+v, %v", res, err)
	}
	// An empty import leaves no marker, so a later real one still runs.
	res, err = db.ImportPhase1(ctx, phase1Sample())
	if err != nil || !res.Done {
		t.Errorf("import after an empty one = %+v, %v", res, err)
	}
}

func TestImportPhase1RejectsBadInput(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*Phase1Import){
		"person id": func(in *Phase1Import) { in.People[0].ID = "Roman!" },
		"key hash":  func(in *Phase1Import) { in.People[0].Keys[0].SHA256 = "XYZ" },
		"uppercase": func(in *Phase1Import) {
			in.People[0].Keys[1].SHA256 = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
		},
		"plugin id":    func(in *Phase1Import) { in.Plugins[0].ID = "TV" },
		"plugin empty": func(in *Phase1Import) { in.Plugins[0].Command = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			db, _ := newTestDB(t)
			in := phase1Sample()
			mutate(&in)
			if _, err := db.ImportPhase1(ctx, in); err == nil {
				t.Fatal("import accepted bad input")
			}
			if list, _ := db.People().List(ctx); len(list) != 0 {
				t.Errorf("a failed import left %d people", len(list))
			}
			// Nothing was marked, so the corrected input imports.
			if res, err := db.ImportPhase1(ctx, phase1Sample()); err != nil || !res.Done {
				t.Errorf("retry = %+v, %v", res, err)
			}
		})
	}
}

func TestImportPhase1IntoExistingHome(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	// An owner and a home name already exist (setup ran first); the import merges instead of clobbering.
	existing := mustPerson(t, db, "tanya", RoleOwner)
	if err := db.Settings().Put(ctx, HomeSettings{HomeName: "Keep me", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	res, err := db.ImportPhase1(ctx, phase1Sample())
	if err != nil {
		t.Fatal(err)
	}
	if want := (ImportResult{Done: true, People: 1, Keys: 2, Plugins: 1}); res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if roman, _ := db.People().Get(ctx, "roman"); roman.Role != RoleMember {
		t.Errorf("roman = %s, want member (an owner already exists)", roman.Role)
	}
	if got, _ := db.People().Get(ctx, "tanya"); got.Role != RoleOwner || string(got.UserHandle) != string(existing.UserHandle) {
		t.Errorf("existing person was changed: %+v", got)
	}
	if s, _ := db.Settings().Get(ctx); s.HomeName != "Keep me" || s.Language != "en" {
		t.Errorf("settings = %+v", s)
	}
}

func TestImportPhase1SkipsKnownKeys(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	in := phase1Sample()
	in.People[1].Keys = []Phase1Key{in.People[0].Keys[0]} // the same hash listed under two people
	res, err := db.ImportPhase1(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys != 2 {
		t.Errorf("keys imported = %d, want 2 (the duplicate hash is skipped)", res.Keys)
	}
	if mine, _ := db.Keys().ListByPerson(ctx, "tanya"); len(mine) != 0 {
		t.Errorf("tanya got a key that already belongs to roman: %+v", mine)
	}
}
