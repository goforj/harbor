//go:build windows

package windowsinstallation

import (
	"context"
	"encoding/json"
	"flag"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var nativeChild = flag.Bool("harbor-native-child", false, "run the explicitly selected native test child")
var nativePublisher = flag.String("harbor-native-publisher", "", "pin the disposable test-only signed fixture publisher")
var nativeNested = flag.Bool("harbor-native-nested", false, "verify another unprivileged launch from the native test child")

// TestNativeAdmittedProcessLaunch proves actual suspended-child admission and a further launch from effective medium authority.
func TestNativeAdmittedProcessLaunch(t *testing.T) {
	root := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT")
	if root == "" {
		t.Skip("disposable signed native fixture was not configured")
	}
	payloads := make(map[string][]byte)
	for extension, variable := range map[string]string{".exe": "HARBOR_ADMISSION_SIGNED_PE", ".ps1": "HARBOR_ADMISSION_SIGNED_SCRIPT", ".cer": "HARBOR_ADMISSION_CERTIFICATE"} {
		data, err := os.ReadFile(os.Getenv(variable))
		if err != nil {
			t.Fatal(err)
		}
		payloads[extension] = data
	}
	publisher := os.Getenv("HARBOR_ADMISSION_PUBLISHER")
	payloads[".txt"] = []byte(publisher)
	path := filepath.Join(root, "launch")
	writeSignedFixture(t, path, publisher, payloads)
	installation, err := admitAt(path, publisher)
	if err != nil {
		t.Fatal(err)
	}
	defer installation.Close()
	child, err := installation.Start("cli", []string{"-test.run=^TestNativeInstalledChild$", "-test.v", "-harbor-native-child", "-harbor-native-nested", "-harbor-native-publisher=" + publisher})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	var actualToken windows.Token
	if err := windows.OpenProcessToken(child.process, windows.TOKEN_QUERY|windows.READ_CONTROL, &actualToken); err != nil {
		t.Fatal(err)
	}
	security, err := windows.GetSecurityInfo(windows.Handle(actualToken), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual child token object descriptor: %s", security.String())
	if err := actualToken.Close(); err != nil {
		t.Fatal(err)
	}
	facts, _ := json.Marshal(child.Facts)
	t.Logf("actual suspended child admitted before resume: %s", facts)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := child.Wait(ctx)
	if err != nil || code != 0 {
		t.Fatalf("native restricted child code=%d err=%v", code, err)
	}
}

// TestNativeInstalledChild independently verifies the running worker without depending on controller token observations.
func TestNativeInstalledChild(t *testing.T) {
	if !*nativeChild {
		t.Skip("only the explicitly selected child runs this assertion")
	}
	facts, err := InspectCurrentToken()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("running installed child: %+v", facts)
	for _, access := range []uint32{windows.TOKEN_QUERY, windows.TOKEN_DUPLICATE, windows.TOKEN_ADJUST_DEFAULT, windows.TOKEN_ALL_ACCESS} {
		var own windows.Token
		err := windows.OpenProcessToken(windows.CurrentProcess(), access, &own)
		t.Logf("running own-token access %#x: %v", access, err)
		if err == nil {
			own.Close()
		}
	}
	if !*nativeNested {
		return
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	installation, err := admitAt(root, *nativePublisher)
	if err != nil {
		t.Fatal(err)
	}
	defer installation.Close()
	child, err := installation.Start("cli", []string{"-test.run=^TestNativeInstalledChild$", "-test.v", "-harbor-native-child"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, err := child.Wait(ctx)
	if err != nil || code != 0 {
		t.Fatalf("nested medium child code=%d err=%v", code, err)
	}
}

// TestInstalledRoleArgumentsRejectArbitraryWorkers keeps task and maintenance capabilities fixed.
func TestInstalledRoleArgumentsRejectArbitraryWorkers(t *testing.T) {
	for _, test := range []struct {
		role      string
		arguments []string
		allowed   bool
	}{
		{"desktop", nil, true}, {"desktop", []string{"foreign"}, false}, {"daemon-launcher", []string{"--installed-worker"}, true}, {"daemon-launcher", []string{"--verify-stopped"}, true}, {"daemon-launcher", []string{"--installed-worker", "foreign.exe"}, false}, {"daemon", []string{"migrate"}, true}, {"daemon", []string{"--foreground"}, true}, {"daemon", []string{"foreign"}, false}, {"helper", nil, false}, {"cli", []string{"daemon", "snapshot"}, true},
	} {
		if allowed := admitRoleArguments(test.role, test.arguments) == nil; allowed != test.allowed {
			t.Fatalf("role %s arguments %v allowed=%v", test.role, test.arguments, allowed)
		}
	}
}
