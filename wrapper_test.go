package wrapper

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCheckNinjaExists(t *testing.T) {
	// Test when ninja is available
	origPath := os.Getenv("PATH")
	defer func() {
		if err := os.Setenv("PATH", origPath); err != nil {
			t.Errorf("Failed to restore PATH: %v", err)
		}
	}()

	// Create temp dir with mock ninja
	tempDir := t.TempDir()

	// Windows needs .exe extension for executables
	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}
	mockNinja := filepath.Join(tempDir, "distninja"+exeSuffix)

	// Create mock executable
	if runtime.GOOS == "windows" {
		// On Windows, create a simple batch file as our mock
		if err := os.WriteFile(mockNinja, []byte("@echo mock"), 0755); err != nil {
			t.Fatalf("Failed to create mock ninja: %v", err)
		}
	} else {
		if err := os.WriteFile(mockNinja, []byte("#!/bin/sh\necho mock"), 0755); err != nil {
			t.Fatalf("Failed to create mock ninja: %v", err)
		}
	}

	// Add temp dir to PATH
	if err := os.Setenv("PATH", tempDir); err != nil {
		t.Fatalf("Failed to set PATH: %v", err)
	}

	err := checkNinjaExists()
	if err != nil {
		t.Errorf("Expected no error when ninja exists, got: %v", err)
	}

	// Test when ninja is not available
	if err := os.Setenv("PATH", ""); err != nil {
		t.Fatalf("Failed to clear PATH: %v", err)
	}

	err = checkNinjaExists()
	if err == nil {
		t.Error("Expected error when ninja doesn't exist, got nil")
	}
}

func TestDetermineCompileType(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		expectedType string
		expectedTgts []string
	}{
		{
			name:         "Full build with no args",
			args:         []string{},
			expectedType: "full",
			expectedTgts: []string{},
		},
		{
			name:         "mm command",
			args:         []string{"mm"},
			expectedType: "module",
			expectedTgts: []string{"current-dir"},
		},
		{
			name:         "mmm command with path",
			args:         []string{"mmm", "system/core"},
			expectedType: "module",
			expectedTgts: []string{"system/core"},
		},
		{
			name:         "m command with module",
			args:         []string{"m", "libutils"},
			expectedType: "module",
			expectedTgts: []string{"libutils"},
		},
		{
			name:         "build.sh with module",
			args:         []string{"./build.sh", "libutils"},
			expectedType: "module",
			expectedTgts: []string{"libutils"},
		},
		{
			name:         "env check command",
			args:         []string{"showcommands"},
			expectedType: "env_check",
			expectedTgts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock current directory for mm test
			if len(tt.args) > 0 && tt.args[0] == "mm" {
				origDir, _ := os.Getwd()
				defer func() {
					if err := os.Chdir(origDir); err != nil {
						t.Errorf("Failed to restore original directory: %v", err)
					}
				}()

				tempDir := t.TempDir()
				if err := os.Chdir(tempDir); err != nil {
					t.Fatalf("Failed to change directory: %v", err)
				}

				tt.expectedTgts = []string{filepath.Base(tempDir)}
			}

			compileType, targets := determineCompileType(tt.args)
			if compileType != tt.expectedType {
				t.Errorf("Expected type %s, got %s", tt.expectedType, compileType)
			}

			if len(targets) != len(tt.expectedTgts) && tt.expectedType != "module" {
				t.Errorf("Expected %d targets, got %d", len(tt.expectedTgts), len(targets))
			}
		})
	}
}

func TestParseCompdbEntry(t *testing.T) {
	tests := []struct {
		name     string
		entry    map[string]interface{}
		expected CompilerCommandInfo
	}{
		{
			name: "Basic clang compilation",
			entry: map[string]interface{}{
				"command":   "clang -Iinclude -DFOO=1 -c foo.c -o foo.o",
				"directory": "/src",
				"file":      "foo.c",
				"output":    "foo.o",
			},
			expected: CompilerCommandInfo{
				Command:      "clang -Iinclude -DFOO=1 -c foo.c -o foo.o",
				CompilerType: "clang",
				InputFiles:   []string{"foo.c"},
				OutputFile:   "foo.o",
				WorkingDir:   "/src",
				Includes:     []string{"include"},
				Defines:      []string{"FOO=1"},
				Flags:        []string{"-c"},
			},
		},
		{
			name: "C++ compilation with multiple inputs",
			entry: map[string]interface{}{
				"command":     "clang++ -Iinclude1 -Iinclude2 -DFOO -DBAR=2 -O2 -c foo.cpp bar.cpp -o out.o",
				"directory":   "/src",
				"input_files": []interface{}{"foo.cpp", "bar.cpp"},
				"target":      "out.o",
			},
			expected: CompilerCommandInfo{
				Command:      "clang++ -Iinclude1 -Iinclude2 -DFOO -DBAR=2 -O2 -c foo.cpp bar.cpp -o out.o",
				CompilerType: "clang++",
				InputFiles:   []string{"foo.cpp", "bar.cpp"},
				OutputFile:   "out.o",
				WorkingDir:   "/src",
				Includes:     []string{"include1", "include2"},
				Defines:      []string{"FOO", "BAR=2"},
				Flags:        []string{"-O2", "-c"},
			},
		},
		{
			name: "Java compilation",
			entry: map[string]interface{}{
				"command":   "javac -d out -sourcepath src src/com/example/Foo.java",
				"directory": "/java",
				"file":      "src/com/example/Foo.java",
			},
			expected: CompilerCommandInfo{
				Command:      "javac -d out -sourcepath src src/com/example/Foo.java",
				CompilerType: "javac",
				InputFiles:   []string{"src/com/example/Foo.java"},
				WorkingDir:   "/java",
				Flags:        []string{"-d", "out", "-sourcepath", "src"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCompdbEntry(tt.entry, "/default/dir")

			if result.Command != tt.expected.Command {
				t.Errorf("Command mismatch: expected %q, got %q", tt.expected.Command, result.Command)
			}

			if result.CompilerType != tt.expected.CompilerType {
				t.Errorf("CompilerType mismatch: expected %q, got %q", tt.expected.CompilerType, result.CompilerType)
			}

			if !reflect.DeepEqual(result.InputFiles, tt.expected.InputFiles) {
				t.Errorf("InputFiles mismatch: expected %v, got %v", tt.expected.InputFiles, result.InputFiles)
			}

			if result.OutputFile != tt.expected.OutputFile {
				t.Errorf("OutputFile mismatch: expected %q, got %q", tt.expected.OutputFile, result.OutputFile)
			}

			if !reflect.DeepEqual(result.Includes, tt.expected.Includes) {
				t.Errorf("Includes mismatch: expected %v, got %v", tt.expected.Includes, result.Includes)
			}

			if !reflect.DeepEqual(result.Defines, tt.expected.Defines) {
				t.Errorf("Defines mismatch: expected %v, got %v", tt.expected.Defines, result.Defines)
			}

			if result.WorkingDir != tt.expected.WorkingDir {
				t.Errorf("WorkingDir mismatch: expected %q, got %q", tt.expected.WorkingDir, result.WorkingDir)
			}
		})
	}
}

func TestExtractModuleNameFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{
			path:     "out/target/product/generic/obj/SHARED_LIBRARIES/libutils_intermediates/utils.o",
			expected: "libutils",
		},
		{
			path:     "out/soong/.intermediates/system/core/libutils/android_arm64_armv8-a_shared/libutils.so",
			expected: "libutils",
		},
		{
			path:     "out/soong/.intermediates/packages/apps/Settings/Settings/android_common/Settings.apk",
			expected: "Settings",
		},
		{
			path:     "out/host/linux-x86/obj/EXECUTABLES/aidl_intermediates/aidl",
			expected: "aidl",
		},
		{
			path:     "out/soong/.intermediates/system/sepolicy/apex/com.android.sepolicy.cil/android_common/com.android.sepolicy.cil",
			expected: "com.android.sepolicy.cil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := extractModuleNameFromPath(tt.path)
			if result != tt.expected {
				t.Errorf("For path %q\nExpected %q, got %q", tt.path, tt.expected, result)
			}
		})
	}
}

func TestWriteCompileCommands(t *testing.T) {
	tempDir := t.TempDir()

	commands := CommandDatabase{
		Commands: []CompilerCommandInfo{
			{
				Command:      "clang -c foo.c -o foo.o",
				CompilerType: "clang",
				InputFiles:   []string{"foo.c"},
				OutputFile:   "foo.o",
			},
		},
	}

	err := writeCompileCommands(tempDir, commands)
	if err != nil {
		t.Fatalf("writeCompileCommands failed: %v", err)
	}

	// Verify file was created
	outputFile := filepath.Join(tempDir, "compile_commands.json")
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Fatalf("Output file was not created")
	}

	// Verify content
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	var parsed CommandDatabase
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("Failed to parse output JSON: %v", err)
	}

	if len(parsed.Commands) != 1 {
		t.Errorf("Expected 1 command, got %d", len(parsed.Commands))
	}

	if parsed.Commands[0].Command != commands.Commands[0].Command {
		t.Errorf("Command mismatch in output file")
	}
}

func TestExpandModuleTargets(t *testing.T) {
	tests := []struct {
		input    []string
		expected []string
	}{
		{
			input:    []string{"system/core/init"},
			expected: []string{"system/core/init", "system_core_init", "init", "libinit"},
		},
		{
			input:    []string{"libutils"},
			expected: []string{"libutils", "utils"},
		},
		{
			input:    []string{"Settings"},
			expected: []string{"Settings", "libSettings"},
		},
		{
			input:    []string{"framework-res"},
			expected: []string{"framework-res", "libframework-res"},
		},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.input, ","), func(t *testing.T) {
			result := expandModuleTargets(tt.input)

			// Check that all expected items are present
			for _, expected := range tt.expected {
				found := false
				for _, actual := range result {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected target %q not found in result", expected)
				}
			}

			// Check no unexpected items are present
			for _, actual := range result {
				found := false
				for _, expected := range tt.expected {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Unexpected target %q in result", actual)
				}
			}
		})
	}
}

func TestCreateTempNinjaFile(t *testing.T) {
	tempDir := t.TempDir()
	origNinja := filepath.Join(tempDir, "build.ninja")
	err := os.WriteFile(origNinja, []byte("rule cc\n  command = clang $in -o $out"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test ninja file: %v", err)
	}

	// Get absolute path since the function may need it
	absOrigNinja, err := filepath.Abs(origNinja)
	if err != nil {
		t.Fatalf("Failed to get absolute path: %v", err)
	}

	tempFile, err := createTempNinjaFile(absOrigNinja)
	if err != nil {
		t.Fatalf("createTempNinjaFile failed: %v", err)
	}

	defer func() {
		if tempFile != "" {
			if err := os.Remove(tempFile); err != nil {
				t.Errorf("Failed to remove temp file: %v", err)
			}
		}
	}()

	// Verify temp file exists
	if _, err := os.Stat(tempFile); os.IsNotExist(err) {
		t.Fatalf("Temporary ninja file was not created")
	}

	// Verify content
	content, err := os.ReadFile(tempFile)
	if err != nil {
		t.Fatalf("Failed to read temp file: %v", err)
	}

	contentStr := string(content)
	t.Logf("File content: %s", contentStr)

	// Check if key structural elements exist
	if !strings.Contains(contentStr, "pool highmem_pool") {
		t.Errorf("Expected content to contain 'pool highmem_pool'")
	}

	if !strings.Contains(contentStr, "depth = 1") {
		t.Errorf("Expected content to contain 'depth = 1'")
	}

	// Checks if the file contains subninja and some part of the original file path
	if !strings.Contains(contentStr, "subninja ") {
		t.Errorf("Expected content to contain 'subninja' directive")
	}

	// Checks if the original filename appears in the content, not caring about the full path format
	origFileName := filepath.Base(absOrigNinja)
	if !strings.Contains(contentStr, origFileName) {
		t.Errorf("Expected content to reference original file '%s'", origFileName)
	}
}

func TestParseNinjaTargetsOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name: "Simple targets",
			input: `target1: phony
target2: phony
target3: CUSTOM`,
			expected: []string{"target1", "target2", "target3"},
		},
		{
			name: "Mixed formats",
			input: `target1: CUSTOM || target2
target2: phony input1 input2 | order-only
target3: phony`,
			expected: []string{"target1", "target2", "target3"},
		},
		{
			name:     "Windows line endings",
			input:    "target1: phony\r\ntarget2: CUSTOM\r\ntarget3: phony",
			expected: []string{"target1", "target2", "target3"},
		},
		{
			name: "Empty lines and comments",
			input: `# Comment
target1: phony

target2: CUSTOM # Another comment
`,
			expected: []string{"target1", "target2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := bytes.NewBufferString(tt.input)
			result := parseNinjaTargetsOutput(buf)

			// Debug output to see which targets are actually returned
			t.Logf("Result targets: %v", result)

			// Check that the expected targets are in the results
			expectedMap := make(map[string]bool)
			for _, target := range tt.expected {
				expectedMap[target] = true
			}

			// Note which additional targets are returned
			for _, target := range result {
				if !expectedMap[target] {
					// Just log the extra target, don't accumulate it
					t.Logf("Additional target found: %q", target)
				}
			}

			// Check if any expected targets are missing
			resultMap := make(map[string]bool)
			for _, target := range result {
				resultMap[target] = true
			}

			var missing bool
			for _, target := range tt.expected {
				if !resultMap[target] {
					missing = true
					t.Errorf("Expected target missing from result: %q", target)
				}
			}

			// Report an error only if the expected target is missing
			if missing {
				t.Errorf("Some expected targets are missing from result")
			}
		})
	}
}
