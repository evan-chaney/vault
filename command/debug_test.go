package command

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/mholt/archiver"
	"github.com/mitchellh/cli"
)

func testDebugCommand(tb testing.TB) (*cli.MockUi, *DebugCommand) {
	tb.Helper()

	ui := cli.NewMockUi()
	return ui, &DebugCommand{
		BaseCommand: &BaseCommand{
			UI: ui,
		},
	}
}

func TestDebugCommand_Run(t *testing.T) {
	t.Parallel()

	testDir, err := ioutil.TempDir("", "vault-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testDir)

	cases := []struct {
		name string
		args []string
		out  string
		code int
	}{
		{
			"valid",
			[]string{
				"-duration=1s",
				fmt.Sprintf("-output=%s/valid", testDir),
			},
			"",
			0,
		},
		{
			"too_many_args",
			[]string{
				"-duration=1s",
				fmt.Sprintf("-output=%s/too_many_args", testDir),
				"foo",
			},
			"Too many arguments",
			1,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, closer := testVaultServer(t)
			defer closer()

			ui, cmd := testDebugCommand(t)
			cmd.client = client
			cmd.skipTimingChecks = true

			code := cmd.Run(tc.args)
			if code != tc.code {
				t.Fatalf("expected %d to be %d", code, tc.code)
			}

			combined := ui.OutputWriter.String() + ui.ErrorWriter.String()
			if !strings.Contains(combined, tc.out) {
				t.Fatalf("expected %q to contain %q", combined, tc.out)
			}
		})
	}
}

func TestDebugCommand_Archive(t *testing.T) {
	t.Parallel()

	// TODO: Switch to TDT, test for not-ext, ext, no-compression cases

	testDir, err := ioutil.TempDir("", "vault-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testDir)

	client, closer := testVaultServer(t)
	defer closer()

	_, cmd := testDebugCommand(t)
	cmd.client = client
	cmd.skipTimingChecks = true

	basePath := "archive"
	args := []string{
		"-duration=1s",
		fmt.Sprintf("-output=%s/%s", testDir, basePath),
		"-target=server-status",
	}

	code := cmd.Run(args)
	if exp := 0; code != exp {
		t.Fatalf("expected %d to be %d", code, exp)
	}

	bundlePath := filepath.Join(testDir, basePath+debugCompressionExt)
	_, err = os.Stat(bundlePath)
	if os.IsNotExist(err) {
		t.Fatal(err)
	}

	tgz := archiver.NewTarGz()
	err = tgz.Walk(bundlePath, func(f archiver.File) error {
		fh, ok := f.Header.(*tar.Header)
		if !ok {
			t.Fatalf("invalid file header: %#v", f.Header)
		}

		// Ignore base directory and index file
		if fh.Name == basePath+"/" || fh.Name == filepath.Join(basePath, "index.json") {
			return nil
		}

		if fh.Name != filepath.Join(basePath, "server_status.json") {
			t.Fatalf("unxexpected file: %s", fh.Name)
		}
		return nil
	})
}

func TestDebugCommand_CaptureTargets(t *testing.T) {
	t.Parallel()

	testDir, err := ioutil.TempDir("", "vault-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testDir)

	cases := []struct {
		name          string
		targets       []string
		expectedFiles []string
	}{
		// TODO: Add case for config target
		{
			"host-info",
			[]string{"host"},
			[]string{"host_info.json"},
		},
		{
			"metrics",
			[]string{"metrics"},
			[]string{"metrics.json"},
		},
		{
			"replication-status",
			[]string{"replication-status"},
			[]string{"replication_status.json"},
		},
		{
			"server-status",
			[]string{"server-status"},
			[]string{"server_status.json"},
		},
		{
			"all-minus-pprof",
			[]string{"host", "metrics", "replication-status", "server-status"},
			[]string{"host_info.json", "metrics.json", "replication_status.json", "server_status.json"},
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, closer := testVaultServer(t)
			defer closer()

			ui, cmd := testDebugCommand(t)
			cmd.client = client
			cmd.skipTimingChecks = true

			basePath := tc.name
			args := []string{
				"-duration=1s",
				fmt.Sprintf("-output=%s/%s", testDir, basePath),
			}
			for _, target := range tc.targets {
				args = append(args, fmt.Sprintf("-target=%s", target))
			}

			code := cmd.Run(args)
			if exp := 0; code != exp {
				t.Log(ui.OutputWriter.String())
				t.Log(ui.ErrorWriter.String())
				t.Fatalf("expected %d to be %d", code, exp)
			}

			bundlePath := filepath.Join(testDir, basePath+debugCompressionExt)
			_, err = os.Open(bundlePath)
			if err != nil {
				t.Fatalf("failed to open archive: %s", err)
			}

			tgz := archiver.NewTarGz()
			err = tgz.Walk(bundlePath, func(f archiver.File) error {
				fh, ok := f.Header.(*tar.Header)
				if !ok {
					t.Fatalf("invalid file header: %#v", f.Header)
				}

				// Ignore base directory and index file
				if fh.Name == basePath+"/" || fh.Name == filepath.Join(basePath, "index.json") {
					return nil
				}

				for _, fileName := range tc.expectedFiles {
					if fh.Name == filepath.Join(basePath, fileName) {
						return nil
					}
				}

				// If we reach here, it means that this is an unexpected file
				return fmt.Errorf("unexpected file: %s", fh.Name)
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDebugCommand_Pprof(t *testing.T) {
	t.Parallel()

	testDir, err := ioutil.TempDir("", "vault-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testDir)

	client, closer := testVaultServer(t)
	defer closer()

	ui, cmd := testDebugCommand(t)
	cmd.client = client
	cmd.skipTimingChecks = true

	basePath := "pprof"
	outputPath := filepath.Join(testDir, basePath)
	// pprof requires a minimum interval of 1s
	args := []string{
		"-compress=false",
		"-duration=1s",
		"-interval=1s",
		fmt.Sprintf("-output=%s", outputPath),
		"-target=pprof",
	}

	code := cmd.Run(args)
	if exp := 0; code != exp {
		t.Log(ui.ErrorWriter.String())
		t.Fatalf("expected %d to be %d", code, exp)
	}

	profiles := []string{"heap.prof", "goroutine.prof"}
	pollingProfiles := []string{"profile.prof", "trace.out"}

	// These are captures on the first (0th) and last (1st) frame
	for _, v := range profiles {
		files, _ := filepath.Glob(fmt.Sprintf("%s/*/%s", outputPath, v))
		if len(files) != 2 {
			t.Errorf("output data should exist for %s: got: %v", v, files)
		}
	}

	// Since profile and trace are polling outputs, these only get captured
	// on the first (0th) frame.
	for _, v := range pollingProfiles {
		files, _ := filepath.Glob(fmt.Sprintf("%s/*/%s", outputPath, v))
		if len(files) != 1 {
			t.Errorf("output data should exist for %s: got: %v", v, files)
		}
	}
}

func TestDebugCommand_IndexFile(t *testing.T) {
	t.Parallel()

	testDir, err := ioutil.TempDir("", "vault-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testDir)

	client, closer := testVaultServer(t)
	defer closer()

	ui, cmd := testDebugCommand(t)
	cmd.client = client
	cmd.skipTimingChecks = true

	basePath := "index-test"
	outputPath := filepath.Join(testDir, basePath)
	// pprof requires a minimum interval of 1s
	args := []string{
		"-compress=false",
		"-duration=1s",
		"-interval=1s",
		"-metrics-interval=1s",
		fmt.Sprintf("-output=%s", outputPath),
	}

	code := cmd.Run(args)
	if exp := 0; code != exp {
		t.Log(ui.ErrorWriter.String())
		t.Fatalf("expected %d to be %d", code, exp)
	}

	content, err := ioutil.ReadFile(filepath.Join(outputPath, "index.json"))
	if err != nil {
		t.Fatal(err)
	}

	index := &debugIndex{}
	if err := json.Unmarshal(content, index); err != nil {
		t.Fatal(err)
	}
	if len(index.Output) == 0 {
		t.Fatalf("expected valid index file: got: %v", index)
	}
}

func TestDebugCommand_TimingChecks(t *testing.T) {
	t.Skip("Not implemented yet")
}

func TestDebugCommand_NoConnection(t *testing.T) {
	t.Parallel()

	client, err := api.NewClient(nil)
	if err != nil {
		t.Fatal(err)
	}

	_, cmd := testDebugCommand(t)
	cmd.client = client
	cmd.skipTimingChecks = true

	args := []string{
		"-duration=1s",
		"-target=server-status",
	}

	code := cmd.Run(args)
	if exp := 1; code != exp {
		t.Fatalf("expected %d to be %d", code, exp)
	}
}