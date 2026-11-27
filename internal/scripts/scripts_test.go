// Package scripts tests the shell scripts under scripts/ (PRD Fase 9): the
// mTLS certificate script and the age-encrypted backup and restore. docker and
// age are replaced by small stand-ins on PATH so the logic runs anywhere that
// has a POSIX sh (CI, Git Bash); a real age or openssl is used when present.
package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func script(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	return filepath.ToSlash(p)
}

func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
}

// run runs `sh script args...` with env added to the environment.
func run(t *testing.T, path []string, env []string, name string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{script(name)}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	if path != nil {
		cmd.Env = append(cmd.Env, "PATH="+strings.Join(path, string(os.PathListSeparator)))
	}
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v", name, err)
		}
		return o.String(), e.String(), ee.ExitCode()
	}
	return o.String(), e.String(), 0
}

// ---- mTLS --------------------------------------------------------------------

func TestMTLSInit(t *testing.T) {
	needSh(t)
	ossl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("no openssl")
	}
	openssl := func(args ...string) (string, error) {
		out, err := exec.Command(ossl, args...).CombinedOutput()
		return string(out), err
	}
	dir := filepath.ToSlash(filepath.Join(t.TempDir(), "mtls"))
	stdout, stderr, code := run(t, nil, nil, "mtls-init.sh", "-d", dir, "-n", "laptop")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"ca.pem", "ca.key", "laptop.crt", "laptop.key"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("missing %s", want)
		}
	}
	// the instructions keep the CA key at home
	if !strings.Contains(stdout, "ONLY the public CA") || strings.Contains(stdout, "scp "+dir+"/ca.key") {
		t.Fatalf("the instructions must send only ca.pem to the VPS:\n%s", stdout)
	}

	crt, ca := filepath.Join(dir, "laptop.crt"), filepath.Join(dir, "ca.pem")
	if out, err := openssl("verify", "-CAfile", ca, "-purpose", "sslclient", crt); err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("openssl verify: %v\n%s", err, out)
	}
	if out, _ := openssl("x509", "-in", crt, "-noout", "-text"); !strings.Contains(out, "TLS Web Client Authentication") || strings.Contains(out, "TLS Web Server Authentication") {
		t.Fatalf("the certificate must be for clientAuth only:\n%s", out)
	}
	// valid for 365 days: still valid after 364, gone after 366
	if _, err := openssl("x509", "-in", crt, "-noout", "-checkend", "31449600"); err != nil { // 364 days
		t.Fatal("the certificate should be valid for 364 more days")
	}
	if _, err := openssl("x509", "-in", crt, "-noout", "-checkend", "31622400"); err == nil { // 366 days
		t.Fatal("the certificate should not be valid for 366 more days")
	}
	// not a server certificate: it does not verify for server use
	if out, err := openssl("verify", "-CAfile", ca, "-purpose", "sslserver", crt); err == nil && strings.Contains(out, ": OK") {
		t.Fatal("a client certificate must not verify as a server certificate")
	}
	// the key is PKCS#8 PEM and matches the certificate
	key, _ := os.ReadFile(filepath.Join(dir, "laptop.key"))
	if !strings.HasPrefix(string(key), "-----BEGIN PRIVATE KEY-----") {
		t.Fatal("the key is not PKCS#8 PEM")
	}
	pubFromKey, _ := openssl("pkey", "-in", filepath.Join(dir, "laptop.key"), "-pubout")
	pubFromCrt, _ := openssl("x509", "-in", crt, "-noout", "-pubkey")
	if strings.TrimSpace(pubFromKey) == "" || strings.TrimSpace(pubFromKey) != strings.TrimSpace(pubFromCrt) {
		t.Fatal("the key does not match the certificate")
	}

	// a second device reuses the CA (nothing changes on the VPS)
	before, _ := os.ReadFile(ca)
	if _, stderr, code := run(t, nil, nil, "mtls-init.sh", "-d", dir, "-n", "tablet"); code != 0 {
		t.Fatalf("second device: %d %s", code, stderr)
	}
	if after, _ := os.ReadFile(ca); !bytes.Equal(before, after) {
		t.Fatal("the CA changed")
	}
	if out, err := openssl("verify", "-CAfile", ca, "-purpose", "sslclient", filepath.Join(dir, "tablet.crt")); err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("second device: %v\n%s", err, out)
	}
	// an existing certificate is not overwritten without --force
	if _, stderr, code := run(t, nil, nil, "mtls-init.sh", "-d", dir, "-n", "tablet"); code == 0 || !strings.Contains(stderr, "--force") {
		t.Fatalf("overwrite: %d %s", code, stderr)
	}
	if _, _, code := run(t, nil, nil, "mtls-init.sh", "-d", dir, "-n", "tablet", "--force"); code != 0 {
		t.Fatal("--force must replace it")
	}
	// a certificate from another CA does not verify against this one
	other := filepath.ToSlash(filepath.Join(t.TempDir(), "other"))
	run(t, nil, nil, "mtls-init.sh", "-d", other, "-n", "laptop")
	if out, err := openssl("verify", "-CAfile", ca, "-purpose", "sslclient", filepath.Join(other, "laptop.crt")); err == nil && strings.Contains(out, ": OK") {
		t.Fatal("a certificate of another CA verified")
	}
	// bad names are refused
	for _, bad := range []string{"../x", "A B", "a;b", ""} {
		if _, _, code := run(t, nil, nil, "mtls-init.sh", "-d", dir, "-n", bad); code == 0 {
			t.Errorf("name %q accepted", bad)
		}
	}
}

// ---- backup and restore ---------------------------------------------------------

const fakeDocker = `#!/bin/sh
# stand-in for: docker exec [-i] [-e VAR] CONTAINER cmd args
shift
while [ "${1#-}" != "$1" ]; do
  case "$1" in -e) shift 2 ;; *) shift ;; esac
done
shift
cmd=$1; shift
case "$cmd" in
  pg_dump) [ -n "${FAKE_PGDUMP_FAIL:-}" ] && exit 3; printf 'PGDMP-fake-dump-data' ;;
  psql) echo "" ;;
  createdb) echo "createdb $*" >> "$FAKE_LOG" ;;
  pg_restore) cat > "$FAKE_RESTORED" ;;
esac
`

// fakeAge "encrypts" with rot13 and tags the file with its recipient; it
// decrypts only with an identity file that holds SECRET-FOR:<recipient>.
const fakeAge = `#!/bin/sh
mode=enc; recip=""; out=""; ident=""; in=""
while [ $# -gt 0 ]; do
  case "$1" in -d) mode=dec; shift ;; -r) recip=$2; shift 2 ;; -o) out=$2; shift 2 ;; -i) ident=$2; shift 2 ;; *) in=$1; shift ;; esac
done
echo "age $mode r=$recip i=$ident" >> "$FAKE_LOG"
if [ "$mode" = enc ]; then
  { printf 'FAKEAGE-FOR:%s\n' "$recip"; tr 'A-Za-z' 'N-ZA-Mn-za-m'; } > "$out"
else
  first=$(head -n 1 "$in")
  want="FAKEAGE-FOR:$(sed 's/^SECRET-FOR://' "$ident" | head -n 1)"
  [ "$first" = "$want" ] || { echo "age: no identity matched any of the file's recipients" >&2; exit 1; }
  tail -n +2 "$in" | tr 'A-Za-z' 'N-ZA-Mn-za-m'
fi
`

type rig struct {
	t        *testing.T
	bin      string // dir with the stand-ins
	dir      string // BACKUP_DIR
	log      string
	restored string
	path     []string
}

func newRig(t *testing.T, withAge bool) *rig {
	needSh(t)
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, bin: filepath.Join(root, "bin"), dir: filepath.ToSlash(filepath.Join(root, "backups")),
		log: filepath.ToSlash(filepath.Join(root, "log")), restored: filepath.ToSlash(filepath.Join(root, "restored"))}
	if err := os.MkdirAll(r.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(r.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("docker", fakeDocker)
	if withAge {
		write("age", fakeAge)
	}
	r.path = append([]string{filepath.ToSlash(r.bin)}, filepath.SplitList(os.Getenv("PATH"))...)
	return r
}

func (r *rig) env(extra ...string) []string {
	return append([]string{"BACKUP_DIR=" + r.dir, "FAKE_LOG=" + r.log, "FAKE_RESTORED=" + r.restored, "PG_DB=tunas"}, extra...)
}

func (r *rig) files() []string {
	ents, _ := os.ReadDir(r.dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestBackupWithoutAgeWarnsAndStaysPlain(t *testing.T) {
	r := newRig(t, false)
	_, stderr, code := run(t, r.path, r.env(), "backup-db.sh")
	fs := r.files()
	if code != 0 || len(fs) != 1 || !strings.HasSuffix(fs[0], ".dump") {
		t.Fatalf("exit %d, files %v, stderr %s", code, fs, stderr)
	}
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "NOT encrypted") {
		t.Fatalf("no warning: %q", stderr)
	}
}

func TestBackupWithAgeIsEncryptedAndLeavesNoPlainFile(t *testing.T) {
	r := newRig(t, true)
	_, stderr, code := run(t, r.path, r.env("AGE_RECIPIENT=age1testrecipient"), "backup-db.sh")
	fs := r.files()
	if code != 0 || len(fs) != 1 || !strings.HasSuffix(fs[0], ".dump.age") {
		t.Fatalf("exit %d, files %v, stderr %s", code, fs, stderr)
	}
	if strings.Contains(stderr, "WARNING") {
		t.Fatalf("an encrypted backup must not warn: %q", stderr)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, fs[0]))
	if strings.Contains(string(b), "PGDMP-fake-dump-data") {
		t.Fatal("the dump is readable in the backup file")
	}
	if log, _ := os.ReadFile(r.log); !strings.Contains(string(log), "age enc r=age1testrecipient") {
		t.Fatalf("age was not asked to encrypt for the recipient: %q", log)
	}
}

func TestBackupFailuresLeaveNothingBehind(t *testing.T) {
	r := newRig(t, true)
	// pg_dump fails while age succeeds: not a backup
	_, stderr, code := run(t, r.path, r.env("AGE_RECIPIENT=age1testrecipient", "FAKE_PGDUMP_FAIL=1"), "backup-db.sh")
	if code == 0 || !strings.Contains(stderr, "pg_dump") || len(r.files()) != 0 {
		t.Fatalf("encrypted: exit %d, files %v, stderr %s", code, r.files(), stderr)
	}
	_, _, code = run(t, r.path, r.env("FAKE_PGDUMP_FAIL=1"), "backup-db.sh")
	if code == 0 || len(r.files()) != 0 {
		t.Fatalf("plain: exit %d, files %v", code, r.files())
	}
	// a private key where the public one belongs is refused
	_, stderr, code = run(t, r.path, r.env("AGE_RECIPIENT=AGE-SECRET-KEY-1ABCDEF"), "backup-db.sh")
	if code != 2 || !strings.Contains(stderr, "public key") {
		t.Fatalf("private key accepted: %d %s", code, stderr)
	}
	// AGE_RECIPIENT set but age missing: fail, never fall back to plain
	noAge := newRig(t, false)
	if _, err := exec.LookPath("age"); err == nil {
		t.Skip("a real age is installed, so it cannot be hidden from PATH")
	}
	_, stderr, code = run(t, noAge.path, noAge.env("AGE_RECIPIENT=age1testrecipient"), "backup-db.sh")
	if code == 0 || !strings.Contains(stderr, "age is not installed") || len(noAge.files()) != 0 {
		t.Fatalf("exit %d, files %v, stderr %s", code, noAge.files(), stderr)
	}
}

func TestBackupKeepsTheNewestMixedBackups(t *testing.T) {
	r := newRig(t, true)
	_ = os.MkdirAll(r.dir, 0o755)
	for _, n := range []string{"tunas-20260101-000000.dump", "tunas-20260102-000000.dump.age", "tunas-20260103-000000.dump", "tunas-20260104-000000.dump.age"} {
		p := filepath.Join(r.dir, n)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		ts := mustTime(t, n)
		_ = os.Chtimes(p, ts, ts)
	}
	if _, stderr, code := run(t, r.path, r.env("AGE_RECIPIENT=age1testrecipient", "KEEP=3"), "backup-db.sh"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	if fs := r.files(); len(fs) != 3 {
		t.Fatalf("KEEP=3 left %v", fs)
	}
}

func TestRestoreEncryptedNeedsThePrivateKey(t *testing.T) {
	r := newRig(t, true)
	if _, _, code := run(t, r.path, r.env("AGE_RECIPIENT=age1abc"), "backup-db.sh"); code != 0 {
		t.Fatal("backup failed")
	}
	enc := filepath.ToSlash(filepath.Join(r.dir, r.files()[0]))
	good := filepath.ToSlash(filepath.Join(filepath.Dir(r.dir), "good.key"))
	bad := filepath.ToSlash(filepath.Join(filepath.Dir(r.dir), "bad.key"))
	_ = os.WriteFile(good, []byte("SECRET-FOR:age1abc\n"), 0o600)
	_ = os.WriteFile(bad, []byte("SECRET-FOR:age1other\n"), 0o600)

	// no private key: refused, nothing created
	_, stderr, code := run(t, r.path, r.env(), "restore-db.sh", enc, "tunas_restore")
	if code != 2 || !strings.Contains(stderr, "encrypted") {
		t.Fatalf("no key: %d %s", code, stderr)
	}
	if log, _ := os.ReadFile(r.log); strings.Contains(string(log), "createdb") {
		t.Fatal("a database was created without the key")
	}
	// wrong key: told clearly, nothing restored
	_, stderr, code = run(t, r.path, r.env(), "restore-db.sh", enc, "tunas_restore", bad)
	if code == 0 || !strings.Contains(stderr, "decryption failed") {
		t.Fatalf("wrong key: %d %s", code, stderr)
	}
	if b, _ := os.ReadFile(r.restored); strings.Contains(string(b), "PGDMP") {
		t.Fatal("data was restored with the wrong key")
	}
	// right key, as the third argument and as AGE_IDENTITY
	for _, tc := range []struct {
		env  []string
		args []string
	}{{nil, []string{enc, "tunas_restore", good}}, {[]string{"AGE_IDENTITY=" + good}, []string{enc, "tunas_restore"}}} {
		_ = os.Remove(r.restored)
		if stdout, stderr, code := run(t, r.path, r.env(tc.env...), "restore-db.sh", tc.args...); code != 0 {
			t.Fatalf("right key: %d %s %s", code, stdout, stderr)
		}
		if b, _ := os.ReadFile(r.restored); string(b) != "PGDMP-fake-dump-data" {
			t.Fatalf("restored %q", b)
		}
	}
}

func TestRestorePlainStillWorks(t *testing.T) {
	r := newRig(t, false)
	if _, _, code := run(t, r.path, r.env(), "backup-db.sh"); code != 0 {
		t.Fatal("backup failed")
	}
	plain := filepath.ToSlash(filepath.Join(r.dir, r.files()[0]))
	if _, stderr, code := run(t, r.path, r.env(), "restore-db.sh", plain, "tunas_restore"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	if b, _ := os.ReadFile(r.restored); string(b) != "PGDMP-fake-dump-data" {
		t.Fatalf("restored %q", b)
	}
	// a bad database name never reaches docker
	if _, _, code := run(t, r.path, r.env(), "restore-db.sh", plain, "x; drop database tunas"); code != 2 {
		t.Fatalf("bad name: %d", code)
	}
}

// With a real age installed, encrypt and decrypt for real (the stand-in docker
// still stands in for postgres).
func TestRealAgeRoundTrip(t *testing.T) {
	needSh(t)
	age, err1 := exec.LookPath("age")
	keygen, err2 := exec.LookPath("age-keygen")
	if err1 != nil || err2 != nil {
		t.Skip("age and age-keygen are not installed")
	}
	_ = age
	r := newRig(t, false) // the real age stays on PATH behind the shim dir
	keyFile := filepath.ToSlash(filepath.Join(filepath.Dir(r.dir), "age.key"))
	out, err := exec.Command(keygen, "-o", keyFile).CombinedOutput()
	if err != nil {
		t.Fatalf("age-keygen: %v %s", err, out)
	}
	var pub string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Public key: ") {
			pub = strings.TrimPrefix(line, "Public key: ")
		}
	}
	if !strings.HasPrefix(pub, "age1") {
		t.Fatalf("no public key in %q", out)
	}
	if _, stderr, code := run(t, r.path, r.env("AGE_RECIPIENT="+pub), "backup-db.sh"); code != 0 {
		t.Fatalf("backup: %d %s", code, stderr)
	}
	enc := filepath.ToSlash(filepath.Join(r.dir, r.files()[0]))
	if b, _ := os.ReadFile(enc); bytes.Contains(b, []byte("PGDMP")) {
		t.Fatal("not encrypted")
	}
	if _, stderr, code := run(t, r.path, r.env(), "restore-db.sh", enc, "tunas_restore", keyFile); code != 0 {
		t.Fatalf("restore: %d %s", code, stderr)
	}
	if b, _ := os.ReadFile(r.restored); string(b) != "PGDMP-fake-dump-data" {
		t.Fatalf("restored %q", b)
	}
	// another key cannot read it
	other := filepath.ToSlash(filepath.Join(filepath.Dir(r.dir), "other.key"))
	if out, err := exec.Command(keygen, "-o", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.Remove(r.restored)
	if _, _, code := run(t, r.path, r.env(), "restore-db.sh", enc, "tunas_restore2", other); code == 0 {
		t.Fatal("another key decrypted the backup")
	}
}

// mustTime reads the stamp out of a backup name (tunas-20260101-000000.dump).
func mustTime(t *testing.T, name string) time.Time {
	t.Helper()
	ts, err := time.Parse("20060102-150405", strings.SplitN(strings.TrimPrefix(name, "tunas-"), ".", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
