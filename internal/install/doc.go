// Package install installs, updates and removes plugins from GitHub releases, and updates the homed binary itself.
//
// It has no daemon or store dependencies: the admin API (T2.3) and the wiring (T2.6) call it and supply Hooks that
// touch the store and the plugin host. Nothing in the package logs, so no token can reach a log line.
//
// # Release conventions (PLAN §5.1)
//
// A plugin release holds GoReleaser archives <binary>_<version>_<os>_<arch>.tar.gz (version without the "v"; 32-bit
// ARM is tried as armv7, armv6, arm), a checksums.txt ("<sha256>  <name>"), and homie-plugin.json:
// {"id","title","description","binary","args":["serve"],"minHubVersion","homepage"}. A homed release has the same
// archives for the binary "homed" and checksums.txt, but no manifest. Archives may be flat or wrapped in a single
// directory.
//
// # Plugin API
//
//	cl := install.NewClient(install.TokenFromEnv())
//	in := &install.Installer{Client: cl, DataDir: dataDir, HubVersion: hubVersion}
//
//	cand, err := in.Resolve(ctx, "devXpro/homie-tv", "latest")     // GET /plugins/preview: release, manifest, asset?, compatible?
//	got, err  := in.Install(ctx, "devXpro/homie-tv", "", "tv", h)  // POST /plugins           (job plugin.install)
//	got, err  := in.Update(ctx, "tv", repo, current, "", false, h) // POST /plugins/{id}/update (job plugin.update)
//	err       := in.Remove("tv", keepData)                       // DELETE /plugins/{id}     (after the plugin stopped)
//	got, err  := in.Locate("tv", "0.2.0")                        // read an installed version back, offline
//	rel, err  := cl.Latest(ctx, repo)                            // check-updates: compare rel.Version with the store's
//
// Install and Update run the job steps of ADMIN-API §3 and report each one through Hooks.Step: resolve, download,
// verify, unpack, (update only) switch, start, and rollback when a start fails. Hooks.Progress carries archive
// download bytes for the job's progress bar. Errors map to job error codes with Code(err): checksum_mismatch,
// no_asset, incompatible, start_failed, rolled_back, downgrade, upstream, internal, plus not_found and up_to_date. A
// release older than the installed one (or the running hub) is ErrDowngrade unless the caller allows it; the admin
// allows it only on the local socket with an explicit flag.
//
// Layout and atomicity. Files land in <data_dir>/plugins/<id>/bin/<version>/. A release is downloaded and unpacked in
// a staging directory .stage-* under <data_dir>/plugins/ and appears with one rename, so a crash never leaves a
// half-unpacked version. The "current version" is not a symlink: it is store.Plugin.Command (the absolute binary
// path plus the manifest args, Installed.Command), Version and PreviousVersion. The atomic switch is therefore the
// Hooks.Switch store update, followed by Hooks.Start (restart the plugin and wait until initialize and tools/list
// answer). If Start fails, Hooks.Rollback restores the previous store row and restarts the old binary, the new
// directory is deleted, and the error wraps ErrStartFailed and ErrRolledBack. If Rollback itself fails both
// directories stay for the operator. After a successful update only the new and the previous version remain on disk.
//
// Install calls only Start (it must create the store row and start the plugin); Update calls Switch, then Start.
//
// # Security
//
// Only https is used, for the API, every asset and every redirect hop (at most 5). The optional token is sent as a
// bearer header to the API host only, never to the asset host, and never appears in an error. The archive's sha256
// must equal its line in the release's checksums.txt before anything is unpacked (ErrChecksumMismatch, or
// ErrNoChecksum when the file or line is missing). Note that checksums.txt comes from the same release, so this
// guards against corruption and a bad mirror, not against a compromised repository; the trust anchor is the repo
// the owner chose in the admin. Unpacking refuses absolute and ".." paths, backslashes, duplicate entries, links of
// any kind (symbolic and hard), devices and FIFOs; every file operation goes through os.Root, which independently
// refuses to leave the target directory, and the stored manifest is written through it too (O_EXCL, no link
// followed). Downloads (256 MiB), unpacked bytes (512 MiB, counted on real decompressed bytes) and entry counts (2000)
// are capped; see Limits. File modes are normalized to 0755 or 0644, so setuid bits never survive.
//
// # Self-update of homed
//
//	u := &install.HubUpdater{Client: cl, Version: hubVersion}
//	rel, newer, err := u.Latest(ctx)             // POST /system/check-update
//	res, err := u.Update(ctx, "", false, hooks)  // job hub.update: resolve, download, verify, replace
//	// then: finish the job, shut down gracefully, os.Exit(install.ExitCodeRestart)
//
// Update downloads homed_<version>_<os>_<arch>.tar.gz, verifies it, unpacks it into a temporary directory, runs
// "<new> version" (must exit 0 within 15 s; disable with SkipSmokeTest) and renames a copy over the running binary
// inside the same directory, keeping the old one as <exe>.prev (HubUpdater.Rollback restores it). The running
// process keeps its old inode, so it can finish the job, answer the last SSE event and exit by itself; Update does
// not exit and does not touch the daemon. A symlinked executable (Homebrew's /opt/homebrew/bin/homed) is resolved
// and its real file replaced; a directory that is not writable gives ErrNotWritable, and then the update must go
// through the package manager ("brew upgrade").
//
// Expected service-manager behaviour: after the exit with ExitCodeRestart (75, non-zero on purpose) the manager
// must start the binary again. launchd: KeepAlive true (the LaunchAgent in docs/DEV.md has it; ThrottleInterval
// bounds restart storms). systemd: Restart=always, or Restart=on-failure (75 counts as a failure, so keep 75 out of
// SuccessExitStatus= and RestartPreventExitStatus=). Docker: a restart policy
// (unless-stopped). Without a manager the process just ends and the user starts homed again.
//
// Only unix-like systems (darwin, linux) are supported: archives are tar.gz and the swap relies on rename over a
// running file.
package install
