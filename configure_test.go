package distribution

import (
	"bytes"
	"fmt"
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
	for _, name := range []string{"configure.sh", "kubectl-forward.sh", "detect-runtime.sh"} {
		script, err := os.ReadFile(filepath.Join("scripts", name)) // #nosec G304 -- names come from the fixed list above.
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "scripts", name), script, 0o600); err != nil { // #nosec G703 -- canonical fixture directory beneath t.TempDir.
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"uname":   "printf '%s\\n' \"${CONFIG_TEST_OS:-Linux}\"",
		"id":      "case \"$1\" in -u) printf '1001\\n';; -g) printf '1002\\n';; *) exit 1;; esac",
		"stat":    "if [ \"${CONFIG_TEST_SOCKET_FAIL:-}\" = 1 ]; then exit 1; fi; case \"${CONFIG_TEST_OS:-Linux}\" in Linux) test \"$1\" = -c || exit 1;; Darwin) test \"$1\" = -f || exit 1;; esac; printf '999\\n'",
		"kubectl": "exit 98",
		"docker":  "if [ \"$1\" = info ]; then case \"$*\" in *Host.Security.Rootless*) exit 1;; esac; echo seccomp; exit; fi; printf '%s\\000' \"$@\" >> \"$CONFIG_TEST_DOCKER_LOG\"; case \"$*\" in *'exec -T cloud printenv CLOUD_DOCKER_IP') echo 172.29.240.254;; esac; if [ -n \"${CONFIG_TEST_STDIN_LOG:-}\" ]; then cat > \"$CONFIG_TEST_STDIN_LOG\"; fi",
		"podman":  "printf '%s\\000' \"$@\" >> \"$CONFIG_TEST_DOCKER_LOG\"",
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

func TestConfigurationPreparesSessionWithoutMutatingDocker(t *testing.T) {
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
	want := append(append([]string{}, prefix...), "exec", "-T", "cloud", "cloud", "clusters", "describe", "name with spaces")
	want = append(want, "compose", "--progress", "quiet")
	want = append(want, prefix[1:]...)
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

func TestContainerKubectlPreservesWorkingDirectoryArgumentsAndStdin(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newConfigurationFixture(t)
			out, err := f.runShell(t, shell, `alias kubectl='echo wrong-kubectl'
source "$CONFIG_TEST_SCRIPT" || exit
printf 'manifest-input' | kubectl apply -f - --namespace 'name with spaces'
`, "CONFIG_TEST_STDIN_LOG="+filepath.Join(f.taskHome, "stdin.log"))
			if err != nil {
				t.Fatalf("%s: %v", out, err)
			}
			data, err := os.ReadFile(f.dockerLog)
			if err != nil {
				t.Fatal(err)
			}
			input, err := os.ReadFile(filepath.Join(f.taskHome, "stdin.log"))
			if err != nil || string(input) != "manifest-input" {
				t.Fatalf("stdin lost: %q %v", input, err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			want := []string{"compose", "--progress", "quiet", "--project-directory", f.root, "--env-file", filepath.Join(f.root, ".env"), "-f", filepath.Join(f.root, "compose.yaml"), "run", "-T", "--rm", "--entrypoint", "kubectl", "--volume", f.taskHome + ":/workspace", "--workdir", "/workspace", "lab", "apply", "-f", "-", "--namespace", "name with spaces"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("argv: %q want %q", got, want)
			}
		})
	}
}

// podmanWrappers returns the session wrappers from configure.sh, set up for rootless Podman
// without running the host preflight.
func podmanWrappers(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile("scripts/configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	start, end := strings.Index(string(script), "    function labcompose"), strings.Index(string(script), "    unalias ")
	if start < 0 || end <= start {
		t.Fatal("wrappers missing")
	}
	return "source \"$CONFIG_TEST_ROOT/scripts/kubectl-forward.sh\"\nCLOUD_LAB_ROOT=\"$CONFIG_TEST_ROOT\"\nCLOUD_LAB_RUNTIME=podman\n" + string(script[start:end])
}

// fakeRootlessPodman makes runtime detection report rootless Podman; every other query prints "true".
func (f configurationFixture) fakeRootlessPodman(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.bin, "podman"), []byte("#!/bin/sh\necho true\n"), 0o700); err != nil { // #nosec G306 -- private executable test fixture, not a data file.
		t.Fatal(err)
	}
}

func TestRootlessWrappersUseAvailableComposeProvider(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		for _, standalone := range []bool{false, true} {
			t.Run(fmt.Sprint(shell, "/", standalone), func(t *testing.T) {
				f := newConfigurationFixture(t)
				if standalone {
					if err := os.Symlink(filepath.Join(f.bin, "podman"), filepath.Join(f.bin, "podman-compose")); err != nil {
						t.Fatal(err)
					}
				}
				body := "PATH=\"$CONFIG_TEST_BIN\"\n" + podmanWrappers(t) + `
labcompose up -d cloud || exit
cloud clusters list || exit
lab load start || exit
kubectl apply -f 'manifest with spaces.yaml'
`
				out, err := f.runShell(t, shell, body, "CONFIG_TEST_ROOT="+f.root, "CONFIG_TEST_BIN="+f.bin)
				if err != nil {
					t.Fatalf("wrappers: %s (%v)", out, err)
				}
				data, err := os.ReadFile(f.dockerLog)
				if err != nil {
					t.Fatal(err)
				}
				prefix := []string{"-f", filepath.Join(f.root, "compose.rootless.yaml")}
				if !standalone {
					prefix = append([]string{"compose"}, prefix...)
				}
				var want []string
				for _, args := range [][]string{
					{"up", "-d", "cloud"},
					{"exec", "-T", "cloud", "cloud", "clusters", "list"},
					{"--profile", "tools", "run", "--rm", "lab", "load", "start"},
					{"--profile", "tools", "run", "-T", "--rm", "--entrypoint", "kubectl", "--volume", f.taskHome + ":/workspace", "--workdir", "/workspace", "lab", "apply", "-f", "manifest with spaces.yaml"},
				} {
					want = append(want, prefix...)
					want = append(want, args...)
				}
				got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("argv: %q want %q", got, want)
				}
			})
		}
	}
}

func TestRootlessConfigurationCgroupVersionCompatibility(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		for _, tc := range []struct{ field, version, want string }{
			{"CgroupsVersion", "v2", "Start the user API socket"},
			{"CGroupsVersion", "v2", "Start the user API socket"},
			{"CgroupsVersion", "v1", "Rootless resource limits require cgroups v2"},
			{"CGroupsVersion", "v1", "Rootless resource limits require cgroups v2"},
			{"unavailable", "", "Cannot read the Podman cgroup version"},
		} {
			t.Run(shell+"/"+tc.field+"/"+tc.version, func(t *testing.T) {
				f := newConfigurationFixture(t)
				f.fakeRootlessPodman(t)
				out, err := f.runShell(t, shell, `function podman {
    if [[ "$*" == 'info --format {{.Host.Security.Rootless}}' ]]; then
        printf 'true\n'
    elif [[ "$*" == "info --format {{.Host.$CONFIG_TEST_CGROUP_FIELD}}" ]]; then
        printf '%s\n' "$CONFIG_TEST_CGROUP_VERSION"
    else
        printf 'cannot evaluate template field\n' >&2
        return 1
    fi
}
source "$CONFIG_TEST_SCRIPT"
test "$?" = 1
`, "CLOUD_RUNTIME=podman", "XDG_RUNTIME_DIR=", "CONFIG_TEST_CGROUP_FIELD="+tc.field, "CONFIG_TEST_CGROUP_VERSION="+tc.version)
				if err != nil || !strings.Contains(string(out), tc.want) {
					t.Fatalf("preflight: %s (%v), want %q", out, err, tc.want)
				}
			})
		}
	}
}

func TestContainerPortForward(t *testing.T) {
	for _, mode := range []string{"bash", "zsh", "podman", "podman-zsh"} {
		for _, tc := range []struct {
			name, command string
			ports         []string
			fail          bool
		}{
			{"basic", "port-forward pod/nginx 18080:80", []string{"18080"}, false},
			{"namespace_and_named_ports", "--context 'class context' -n default port-forward service/nginx 18080:http 18443:https --address localhost --pod-running-timeout 1m", []string{"18080", "18443"}, false},
			{"same_port", "port-forward pod/nginx 8080", []string{"8080"}, false},
			{"separator", "port-forward pod/nginx -- 18080:80", []string{"18080"}, false},
			{"random", "port-forward pod/nginx :80", nil, true},
			{"zero", "port-forward pod/nginx 0:80", nil, true},
			{"range", "port-forward pod/nginx 65536:80", nil, true},
			{"overflow", "port-forward pod/nginx 9999999999999999999999:80", nil, true},
			{"external_address", "port-forward pod/nginx 18080:80 --address=0.0.0.0", nil, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				f := newConfigurationFixture(t)
				shell, setup := mode, "source \"$CONFIG_TEST_SCRIPT\" || exit\n"
				if strings.HasPrefix(mode, "podman") {
					shell = map[string]string{"podman": "bash", "podman-zsh": "zsh"}[mode]
					setup = podmanWrappers(t)
				}
				out, err := f.runShell(t, shell, setup+"\nkubectl "+tc.command, "CONFIG_TEST_ROOT="+f.root)
				if tc.fail {
					if err == nil || !strings.Contains(string(out), "Containerized port-forward") {
						t.Fatalf("expected validation error: %s (%v)", out, err)
					}
					if _, err := os.Stat(f.dockerLog); !os.IsNotExist(err) {
						t.Fatal("invalid request invoked runtime")
					}
					return
				}
				if err != nil {
					t.Fatalf("forward: %s (%v)", out, err)
				}
				data, err := os.ReadFile(f.dockerLog)
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
				joined := strings.Join(args, "|")
				for _, port := range tc.ports {
					if !strings.Contains(joined, "--publish|127.0.0.1:"+port+":"+port+"|") {
						t.Fatalf("missing loopback mapping: %q", args)
					}
				}
				for _, want := range []string{"--no-deps", "kubectl-forward", "port-forward|--address=0.0.0.0", "--volume|" + f.taskHome + ":/workspace"} {
					if !strings.Contains(joined, want) {
						t.Fatalf("missing %s: %q", want, args)
					}
				}
				if tc.name == "namespace_and_named_ports" && !strings.Contains(joined, "--context|class context|-n|default|") {
					t.Fatalf("lost flags: %q", args)
				}
			})
		}
	}
}

func TestRootlessConfigurationAddsUserBinOnce(t *testing.T) {
	for _, alreadyPresent := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyPresent), func(t *testing.T) {
			f := newConfigurationFixture(t)
			f.fakeRootlessPodman(t)
			setup := ""
			if alreadyPresent {
				setup = `PATH="$PATH:$HOME/.local/bin"`
			}
			out, err := f.run(t, setup+`
before=$PATH
# The fake Podman fails preflight; PATH must already support user-installed tools.
source "$CONFIG_TEST_SCRIPT" >/dev/null 2>&1 || true
first=$PATH
source "$CONFIG_TEST_SCRIPT" >/dev/null 2>&1 || true
[[ "$PATH" == "$first" ]] || exit 21
case ":$before:" in
 *":$HOME/.local/bin:"*) [[ "$PATH" == "$before" ]] || exit 22 ;;
 *) [[ "$PATH" == "$HOME/.local/bin:$before" ]] || exit 23 ;;
esac
bash -c '[[ ":$PATH:" == *":$HOME/.local/bin:"* ]]' || exit 24
`, "CLOUD_RUNTIME=podman")
			if err != nil {
				t.Fatalf("user PATH setup: %s (%v)", out, err)
			}
		})
	}
}

func TestRootlessComposeInstallation(t *testing.T) {
	script, err := os.ReadFile("scripts/configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(script), "        # Install only when")
	end := strings.Index(string(script), "    else\n        case $(uname -s) in")
	if start < 0 || end <= start {
		t.Fatal("missing provider setup")
	}
	for _, tc := range []struct {
		name, setup   string
		install, fail bool
	}{
		{"standalone", `function podman-compose { :; }; function podman { return 1; }`, false, false},
		{"delegated", `function podman { return 0; }`, false, false},
		{"install", `function podman { return 1; }; function python3 { if [[ "$*" == '-m pip --version' ]]; then return 0; fi; printf 'INSTALL_CALLED\n'; [[ "$*" == '-m pip install --user podman-compose>=1,<2' ]] || return 9; function podman-compose { :; }; }`, true, false},
		{"missing_pip", `function podman { return 1; }; function python3 { return 1; }`, false, true},
		{"install_failed", `function podman { return 1; }; function python3 { if [[ "$*" == '-m pip --version' ]]; then return 0; fi; printf 'INSTALL_CALLED\n'; return 1; }`, true, true},
		{"missing_after_install", `function podman { return 1; }; function python3 { if [[ "$*" == '-m pip --version' ]]; then return 0; fi; printf 'INSTALL_CALLED\n'; }`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfigurationFixture(t)
			body := "PATH=/nonexistent\n" + tc.setup + "\n_probe() {\n" + string(script[start:end]) + "\n}\n_probe\n"
			out, err := f.run(t, body)
			if (err != nil) != tc.fail || strings.Contains(string(out), "INSTALL_CALLED") != tc.install {
				t.Fatalf("setup: %s (%v)", out, err)
			}
		})
	}
}

func TestConfigurationDetectsPodmanWithoutDockerState(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newConfigurationFixture(t)
			if err := os.WriteFile(filepath.Join(f.bin, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- private executable test fixture, not a data file.
				t.Fatal(err)
			}
			f.fakeRootlessPodman(t)
			// The fake reports cgroup "true", so the Podman preflight stops before defining wrappers.
			out, err := f.runShell(t, shell, `source "$CONFIG_TEST_SCRIPT"
[[ $? == 1 && ! -e "$CONFIG_TEST_ROOT/.env" && -z "${KUBECONFIG:-}" ]] || exit 9
`, "CONFIG_TEST_ROOT="+f.root, "KUBECONFIG=")
			if err != nil || !strings.Contains(string(out), "Detected runtime: podman") || !strings.Contains(string(out), "cgroups v2") {
				t.Fatalf("Podman routing: %s (%v)", out, err)
			}
		})
	}
}

func TestConfigurationRejectsDockerRootlessBeforeWriting(t *testing.T) {
	f := newConfigurationFixture(t)
	body := "#!/bin/sh\ncase \"$*\" in *Host.Security.Rootless*) exit 1;; esac\necho rootless\n"
	if err := os.WriteFile(filepath.Join(f.bin, "docker"), []byte(body), 0o700); err != nil { // #nosec G306 -- private executable test fixture, not a data file.
		t.Fatal(err)
	}
	out, err := f.run(t, `source "$CONFIG_TEST_SCRIPT"
result=$?
[[ "$result" == 1 && ! -e "$CONFIG_TEST_ROOT/.env" ]] || exit 9
`, "CONFIG_TEST_ROOT="+f.root)
	if err != nil || !strings.Contains(string(out), "Docker rootless") {
		t.Fatalf("unsupported mode: %s (%v)", out, err)
	}
}

func TestInstanceAutoscalingWrappersRunOnlyOptionalServices(t *testing.T) {
	for _, runtime := range []string{"docker", "podman"} {
		t.Run(runtime, func(t *testing.T) {
			f := newConfigurationFixture(t)
			setup := "source \"$CONFIG_TEST_SCRIPT\"\n"
			prefix := []string{"compose", "--project-directory", f.root, "--env-file", filepath.Join(f.root, ".env"), "-f", filepath.Join(f.root, "compose.yaml")}
			if runtime == "podman" {
				setup = podmanWrappers(t)
				prefix = []string{"compose", "-f", filepath.Join(f.root, "compose.rootless.yaml")}
				stub := `#!/bin/sh
printf '%s\000' "$@" >> "$CONFIG_TEST_DOCKER_LOG"
if [ "$*" = "compose -f $CONFIG_TEST_ROOT/compose.rootless.yaml exec -T cloud printenv CLOUD_DOCKER_IP" ]; then echo 172.29.240.254; fi
if [ "$1" = ps ]; then
    if [ "$2" = -a ]; then
        printf '%s\n' 'cloud-id cloud' 'lb-id instance-lb' 'metrics-id instance-metrics' 'policy-id instance-autoscaler' 'other-id portal'
    else
        printf '%s\n' fixture-project
    fi
fi
`
				if err := os.WriteFile(filepath.Join(f.bin, "podman"), []byte(stub), 0o700); err != nil { // #nosec G306 -- private executable fixture.
					t.Fatal(err)
				}

			}
			out, err := f.run(t, setup+`
lab install instance-autoscaling || exit
cloud groups create web --bootstrap - </dev/null || exit
lab uninstall instance-autoscaling
`, "CONFIG_TEST_ROOT="+f.root)
			if err != nil {
				t.Fatalf("wrappers: %s (%v)", out, err)
			}
			data, err := os.ReadFile(f.dockerLog)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			compose := func(args ...string) {
				want = append(want, prefix...)
				want = append(want, args...)
			}
			compose("exec", "-T", "cloud", "cloud", "instance-autoscaling", "prepare")
			compose("exec", "-T", "cloud", "printenv", "CLOUD_DOCKER_IP")
			compose("--profile", "instance-autoscaling", "up", "-d", "--no-deps", "--build", "instance-lb", "instance-metrics", "instance-autoscaler")
			compose("exec", "-T", "cloud", "cloud", "instance-autoscaling", "activate")
			compose("exec", "-T", "cloud", "cloud", "groups", "create", "web", "--bootstrap", "-")
			compose("exec", "-T", "cloud", "cloud", "instance-autoscaling", "begin-removal")
			if runtime == "podman" {
				want = append(want, "ps", "--filter", "network=minikube-cloud-lab-rootless", "--filter", "label=com.docker.compose.service=cloud", "--format", `{{.Label "com.docker.compose.project"}}`)
				want = append(want, "ps", "-a", "--filter", "label=com.docker.compose.project=fixture-project", "--format", `{{.ID}} {{.Label "com.docker.compose.service"}}`)
				want = append(want, "rm", "--ignore", "-f", "-v", "lb-id", "metrics-id", "policy-id")
			} else {
				compose("--profile", "instance-autoscaling", "stop", "instance-lb", "instance-metrics", "instance-autoscaler")
				compose("--profile", "instance-autoscaling", "rm", "-f", "-v", "instance-lb", "instance-metrics", "instance-autoscaler")
			}
			compose("exec", "-T", "cloud", "cloud", "instance-autoscaling", "finish-removal")

			got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("argv: %q want %q", got, want)
			}
		})
	}
}

func TestGroupBootstrapFromStudentFilesystem(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newConfigurationFixture(t)
			out, err := f.runShell(t, shell, `source "$CONFIG_TEST_SCRIPT" || exit
set -u
cloud instances list || exit
printf 'sample\n' > 'local bootstrap'
for form in separate equals stdin; do
    case $form in
      separate) cloud groups create web --bootstrap 'local bootstrap' --desired 2 ;;
      equals) cloud --api-url http://cloud groups create web '--bootstrap=local bootstrap' ;;
      stdin) cloud groups create web --bootstrap - < 'local bootstrap' ;;
    esac || exit 7
    cmp 'local bootstrap' "$CONFIG_TEST_STDIN_LOG" || exit 8
done
if cloud groups create web --bootstrap missing-file; then exit 9; fi
`, "CONFIG_TEST_STDIN_LOG="+filepath.Join(f.taskHome, "input"))
			if err != nil {
				t.Fatalf("bootstrap forwarding: %s (%v)", out, err)
			}
			data, err := os.ReadFile(f.dockerLog)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.ReplaceAll(string(data), "\x00", " ")
			for _, want := range []string{"groups create web --bootstrap - --desired 2", "--api-url http://cloud groups create web --bootstrap -"} {
				if !strings.Contains(args, want) {
					t.Fatalf("missing %q in %s", want, args)
				}
			}
		})
	}
}

func TestUserDataFileThroughCompose(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		f := newConfigurationFixture(t)
		body := podmanWrappers(t) + `
labcompose() { echo "$*"; cat; }
printf 'file contents\n' > 'user data.yaml'
cloud instances create vm --user-data 'user data.yaml'
`
		out, err := f.runShell(t, shell, body, "CONFIG_TEST_ROOT="+f.root)
		if err != nil || !strings.Contains(string(out), "exec -T cloud cloud instances create vm --user-data -\nfile contents") {
			t.Fatalf("%s: %s %v", shell, out, err)
		}
	}
}
