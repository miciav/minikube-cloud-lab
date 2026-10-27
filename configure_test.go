package distribution

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type configurationFixture struct{ root, taskHome, bin, dockerLog string }

func newConfigurationFixture(t *testing.T) configurationFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := configurationFixture{root: filepath.Join(base, "checkout with spaces"), taskHome: filepath.Join(base, "student home"), bin: filepath.Join(base, "bin"), dockerLog: filepath.Join(base, "docker.log")}
	for _, dir := range []string{filepath.Join(f.root, "scripts"), f.taskHome, f.bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("scripts/configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "scripts/configure.sh"), script, 0o600); err != nil { // #nosec G703 -- canonical fixture directory beneath t.TempDir.
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"uname":  "printf '%s\\n' \"${CONFIG_TEST_OS:-Linux}\"",
		"id":     "case \"$1\" in -u) printf '1001\\n';; -g) printf '1002\\n';; *) exit 1;; esac",
		"stat":   "if [ \"${CONFIG_TEST_SOCKET_FAIL:-}\" = 1 ]; then exit 1; fi; case \"${CONFIG_TEST_OS:-Linux}\" in Linux) test \"$1\" = -c || exit 1;; Darwin) test \"$1\" = -f || exit 1;; esac; printf '999\\n'",
		"docker": "printf '%s\\000' \"$@\" >> \"$CONFIG_TEST_DOCKER_LOG\"",
	} {
		if err := os.WriteFile(filepath.Join(f.bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	} // #nosec G306 -- executable command fixtures under t.TempDir.
	return f
}

func (f configurationFixture) run(t *testing.T, body string, extra ...string) ([]byte, error) {
	t.Helper()
	return f.runShell(t, "bash", body, extra...)
}

func (f configurationFixture) runShell(t *testing.T, shell, body string, extra ...string) ([]byte, error) {
	t.Helper()
	args := []string{"-c", body}
	if shell == "zsh" {
		args = append([]string{"-f"}, args...)
	}
	cmd := exec.CommandContext(t.Context(), shell, args...) // #nosec G204 -- shell and body are test-owned literals.
	cmd.Dir = f.taskHome
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "HOME" && name != "PATH" && name != "BASH_ENV" && name != "ENV" && name != "ZDOTDIR" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+f.taskHome, "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CONFIG_TEST_SCRIPT="+filepath.Join(f.root, "scripts/configure.sh"), "CONFIG_TEST_DOCKER_LOG="+f.dockerLog)
	cmd.Env = append(cmd.Env, extra...)
	return cmd.CombinedOutput()
}

func TestConfigurationPreparesSessionWithoutRunningDocker(t *testing.T) {
	f := newConfigurationFixture(t)
	out, err := f.run(t, `before_dir=$PWD
before_flags=$-
source "$CONFIG_TEST_SCRIPT" || exit
[[ "$PWD" == "$before_dir" && "$-" == "$before_flags" ]] || exit 8
[[ "$KUBECONFIG" == "$HOME/.kube/config" ]] || exit 9
[[ $(type -t cloud) == function && $(type -t lab) == function ]] || exit 10
`)
	if err != nil {
		t.Fatalf("configure: %s (%v)", out, err)
	}
	for _, name := range []string{".kube", ".minikube-cloud-lab"} {
		if info, err := os.Stat(filepath.Join(f.taskHome, name)); err != nil || !info.IsDir() {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"UID=1001", "GID=1002", "DOCKER_GID=999"} {
		if !strings.Contains(string(data), line+"\n") {
			t.Fatalf("missing identity %s", line)
		}
	}
	if _, err := os.Stat(f.dockerLog); !os.IsNotExist(err) {
		t.Fatal("configuration invoked Docker")
	}
}

func TestConfigurationRerunPreservesOverridesAndDoesNotEvaluateEnv(t *testing.T) {
	f := newConfigurationFixture(t)
	marker := filepath.Join(f.taskHome, "must-not-exist")
	original := "CLOUD_IMAGE=example/cloud:custom\nUID=77\nCUSTOM=$(touch '" + marker + "')"
	path := filepath.Join(f.root, ".env")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := f.run(t, `source "$CONFIG_TEST_SCRIPT"`); err != nil {
		t.Fatalf("%s (%v)", out, err)
	}
	first, err := os.ReadFile(path) // #nosec G304 -- fixture file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(first), original) || strings.Contains(string(first), "UID=1001") {
		t.Fatal("existing configuration replaced")
	}
	if out, err := f.run(t, `source "$CONFIG_TEST_SCRIPT"`); err != nil {
		t.Fatalf("%s (%v)", out, err)
	}
	second, err := os.ReadFile(path) // #nosec G304 -- fixture file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("rerun added duplicate configuration")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal(".env was evaluated as shell")
	}
}

func TestConfigurationWrappersReplaceAliasesAndPreserveArguments(t *testing.T) {
	f := newConfigurationFixture(t)
	out, err := f.run(t, `shopt -s expand_aliases
alias cloud='echo wrong-cloud'
alias lab='echo wrong-lab'
source "$CONFIG_TEST_SCRIPT" || exit
cloud clusters describe 'name with spaces'
lab load start --replicas 4
`)
	if err != nil {
		t.Fatalf("wrappers: %s (%v)", out, err)
	}
	assertConfigurationWrapperArguments(t, f)
}

func assertConfigurationWrapperArguments(t *testing.T, f configurationFixture) {
	t.Helper()
	data, err := os.ReadFile(f.dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	prefix := []string{"compose", "--project-directory", f.root, "--env-file", filepath.Join(f.root, ".env"), "-f", filepath.Join(f.root, "compose.yaml")}
	want := append(append(append([]string{}, prefix...), "exec", "cloud", "cloud", "clusters", "describe", "name with spaces"), prefix...)
	want = append(want, "run", "--rm", "lab", "load", "start", "--replicas", "4")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapper argv: %q want %q", got, want)
	}
}

func TestConfigurationFailuresLeaveInteractiveShellAlive(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
	}{{"platform", []string{"CONFIG_TEST_OS=FreeBSD"}}, {"socket", []string{"CONFIG_TEST_SOCKET_FAIL=1"}}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfigurationFixture(t)
			out, err := f.run(t, `source "$CONFIG_TEST_SCRIPT"
status=$?
printf 'ALIVE:%s\n' "$status"
`, tc.env...)
			if err != nil || !strings.Contains(string(out), "ALIVE:1") {
				t.Fatalf("caller did not survive failure: %s (%v)", out, err)
			}
			if _, err := os.Stat(filepath.Join(f.root, ".env")); !os.IsNotExist(err) {
				t.Fatal("failed configuration wrote .env")
			}
		})
	}
}

func TestConfigurationShellsAndPlatforms(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		for _, platform := range []string{"Linux", "Darwin"} {
			t.Run(shell+"/"+platform, func(t *testing.T) {
				f := newConfigurationFixture(t)
				original := "CLOUD_IMAGE=example/cloud:custom\nUID=77\n"
				if err := os.WriteFile(filepath.Join(f.root, ".env"), []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				out, err := f.runShell(t, shell, `CDPATH= cd -- "$(dirname -- "$CONFIG_TEST_SCRIPT")/.." || exit
export CDPATH=.
if [ -n "${BASH_VERSION:-}" ]; then shopt -s expand_aliases; fi
alias cloud='echo wrong-cloud'
alias lab='echo wrong-lab'
before_dir=$PWD
before_flags=$-
source scripts/configure.sh || exit
source scripts/configure.sh || exit
[[ "$PWD" == "$before_dir" && "$-" == "$before_flags" && "$CDPATH" == . ]] || exit 8
[[ "$KUBECONFIG" == "$HOME/.kube/config" && "$CLOUD_LAB_ROOT" == "$before_dir" ]] || exit 9
typeset -f cloud lab >/dev/null || exit 10
[[ ! -e "$CONFIG_TEST_DOCKER_LOG" ]] || exit 11
cloud clusters describe 'name with spaces'
lab load start --replicas 4
`, "CONFIG_TEST_OS="+platform)
				if err != nil {
					t.Fatalf("configure: %s (%v)", out, err)
				}
				data, err := os.ReadFile(filepath.Join(f.root, ".env"))
				if err != nil {
					t.Fatal(err)
				}
				group := "999"
				if platform == "Darwin" {
					group = "0"
				}
				if !strings.HasPrefix(string(data), original) || strings.Count(string(data), "UID=") != 1 || strings.Count(string(data), "\nGID=1002\n") != 1 || strings.Count(string(data), "\nDOCKER_GID="+group+"\n") != 1 {
					t.Fatalf("configuration changed or duplicated: %q", data)
				}
				assertConfigurationWrapperArguments(t, f)
			})
		}
	}
}

func TestZshConfigurationFailuresAndExecutionLeaveSessionAlive(t *testing.T) {
	for _, mode := range []string{"platform", "socket", "execution"} {
		t.Run(mode, func(t *testing.T) {
			f := newConfigurationFixture(t)
			extra := []string{"CONFIG_TEST_OS=Darwin"}
			body := `source "$CONFIG_TEST_SCRIPT"`
			switch mode {
			case "platform":
				extra = []string{"CONFIG_TEST_OS=FreeBSD"}
			case "socket":
				extra = append(extra, "CONFIG_TEST_SOCKET_FAIL=1")
			case "execution":
				body = `zsh -f "$CONFIG_TEST_SCRIPT"`
			}
			out, err := f.runShell(t, "zsh", body+"\nsource_exit=$?\nprintf 'ALIVE:%s\\n' \"$source_exit\"\n", extra...)
			if err != nil || !strings.Contains(string(out), "ALIVE:1") {
				t.Fatalf("caller did not survive failure: %s (%v)", out, err)
			}
			if _, err := os.Stat(filepath.Join(f.root, ".env")); !os.IsNotExist(err) {
				t.Fatal("failed configuration wrote .env")
			}
		})
	}
}

func TestConfigurationMustBeSourced(t *testing.T) {
	f := newConfigurationFixture(t)
	out, err := f.run(t, `bash "$CONFIG_TEST_SCRIPT"`)
	if err == nil || !strings.Contains(string(out), "source") {
		t.Fatalf("missing source instruction: %s (%v)", out, err)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".env")); !os.IsNotExist(err) {
		t.Fatal("execution unexpectedly configured files")
	}
}

func TestConfigurationRelativeSourcePreservesCDPATH(t *testing.T) {
	f := newConfigurationFixture(t)
	out, err := f.run(t, `CDPATH= cd -- "$(dirname -- "$CONFIG_TEST_SCRIPT")/.." || exit
before_dir=$PWD
export CDPATH=.
source scripts/configure.sh || exit
[[ "$CLOUD_LAB_ROOT" == "$before_dir" && "$PWD" == "$before_dir" && "$CDPATH" == . ]] || exit 8
`)
	if err != nil {
		t.Fatalf("relative source with CDPATH: %s (%v)", out, err)
	}
}

func TestConfigurationIgnoresShellDirectoryOutput(t *testing.T) {
	for _, tc := range []struct{ name, shell, setup string }{
		{"zsh-chpwd", "zsh", `function chpwd { print -r -- '~/cloud-lab'; }`},
		{"zsh-cd-function", "zsh", `function cd { builtin cd "$@" || return; print -r -- 'cd output'; }`},
		{"bash-cd-function", "bash", `function cd { builtin cd "$@" || return; printf 'cd output\n'; }`},
		{"zsh-pwd-function", "zsh", `function pwd { print -r -- 'wrong path'; }`},
		{"bash-pwd-function", "bash", `function pwd { printf 'wrong path\n'; }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfigurationFixture(t)
			body := tc.setup + `
source "$CONFIG_TEST_SCRIPT" || exit
[[ "$CLOUD_LAB_ROOT" == "$CONFIG_TEST_ROOT" ]] || exit 9
cloud clusters list
`
			out, err := f.runShell(t, tc.shell, body, "CONFIG_TEST_ROOT="+f.root)
			if err != nil {
				t.Fatalf("configuration with shell customization failed: %s (%v)", out, err)
			}
			if _, err := os.Stat(filepath.Join(f.root, ".env")); err != nil {
				t.Fatalf("configuration did not create .env in checkout: %v", err)
			}
			args, err := os.ReadFile(f.dockerLog)
			if err != nil || !bytes.Contains(args, []byte(f.root+string(os.PathSeparator)+".env")) {
				t.Fatalf("wrapper did not use checkout .env: %q (%v)", args, err)
			}
		})
	}
}
