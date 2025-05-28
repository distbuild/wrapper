package wrapper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type WrapperConfig struct {
	OutDir            string
	SoongOutDir       string
	SourceRootDirs    []string
	BuildArguments    []string
	HighmemParallel   int
	SoongNinjaFile    string
	CombinedNinjaFile string
	NinjaTool         string
}

type CompilerCommandInfo struct {
	Command      string   `json:"command"`      // Original complete command
	CompilerType string   `json:"compilerType"` // Compiler type: clang, gcc, javac, etc.
	InputFiles   []string `json:"inputFiles"`   // Input files list
	OutputFile   string   `json:"outputFile"`   // Output file
	Flags        []string `json:"flags"`        // Compilation flags
	Includes     []string `json:"includes"`     // Include paths
	Defines      []string `json:"defines"`      // Macro definitions
	WorkingDir   string   `json:"workingDir"`   // Working directory
	Module       string   `json:"module"`       // Module name
}

// CommandDatabase stores all intercepted compile commands
type CommandDatabase struct {
	Commands []CompilerCommandInfo `json:"commands"`
}

func GetBuildConfig(OutDir, SoongOutDir string, SourceRootDir, Arguments []string, HighmemParallel int, SoongNinjaFile, CombinedNinjaFile, NinjaTool string) WrapperConfig {
	return WrapperConfig{
		OutDir:            OutDir,
		SoongOutDir:       SoongOutDir,
		SourceRootDirs:    SourceRootDir,
		BuildArguments:    Arguments,
		HighmemParallel:   HighmemParallel,
		SoongNinjaFile:    SoongNinjaFile,
		CombinedNinjaFile: CombinedNinjaFile,
		NinjaTool:         NinjaTool,
	}
}

// RunNinjaWithCommandLogging runs ninja and intercepts compile commands
func RunNinjaWithCommandLogging(ctx context.Context, config WrapperConfig, _ bool) {
	err := checkNinjaExists()
	if err != nil {
		println(err.Error())
		return
	}
	config.NinjaTool = "distninja"

	tempNinjaFile, err := createTempNinjaFile(config.SoongNinjaFile)
	if err != nil {
		fmt.Printf("Error: Failed to create temporary ninja file: %v\n", err)
		return
	}
	BuildTop := os.Getenv("ANDROID_BUILD_TOP")
	tempNinjaFile = filepath.Join(BuildTop, tempNinjaFile)
	fmt.Printf("Temporary ninja file: %s\n", tempNinjaFile)

	commands := CommandDatabase{Commands: []CompilerCommandInfo{}}

	// Clearly distinguish between full build (m) and module build (mm/mmm)
	compileType, moduleTargets := determineCompileType(config.BuildArguments)
	fmt.Printf("Detected compile type: %s\n", compileType)

	if compileType == "full" {
		//Full build (m): process all targets
		fmt.Printf("Full build mode (m): generating complete compilation database\n")
		commands = getAllCompilationCommands(ctx, config, tempNinjaFile)
		fmt.Printf("Extracted %d compilation commands\n", len(commands.Commands))
	} else {
		// Module build (mm/mmm): only process targets related to specified modules
		fmt.Printf("Module build mode (%s): %s\n",
			strings.Join(config.BuildArguments, " "),
			strings.Join(moduleTargets, ", "))

		// Check if module targets exist
		if len(moduleTargets) == 0 {
			// Try to get module targets from current directory or environment variablese
			moduleTargets = detectModuleTargets()
			fmt.Printf("Detected module targets: %s\n", strings.Join(moduleTargets, ", "))
		}

		//  Get targets related to modules
		moduleTargets = expandModuleTargets(moduleTargets)
		fmt.Printf("Expanded module targets: %s\n", strings.Join(moduleTargets, ", "))

		// Find ninja targets related to module
		module := strings.Join(config.BuildArguments, " ")
		relevantTargets := getRelevantTargets(ctx, config, tempNinjaFile, module)

		if len(relevantTargets) > 0 {
			// Find ninja targets related to modules
			commands = getCompilationDatabase(ctx, config, tempNinjaFile, relevantTargets)
			fmt.Printf("Extracted %d compilation commands for modules\n", len(commands.Commands))
		} else {
			fmt.Printf("No ninja targets found for modules, trying fallbacks\n")
			relevantTargets = findNinjaTargetsByFuzzyMatch(ctx, config, tempNinjaFile, moduleTargets)
			if len(relevantTargets) > 0 {
				commands = getCompilationDatabase(ctx, config, tempNinjaFile, relevantTargets)
			}
		}

	}

	if err := writeCompileCommands(config.OutDir, commands); err != nil {
		fmt.Printf("Error: Failed to write compilation command database: %v\n", err)
	} else {
		fmt.Printf("Compilation command database has been written to: %s/compile_commands.json\n", config.OutDir)
	}
}

func checkNinjaExists() error {
	_, err := exec.LookPath("distninja")
	if err != nil {
		return fmt.Errorf("distninja tool not found, please install ninja build tool first")
	}
	return nil
}

// determineCompileType precisely distinguishes between full build and module build
func determineCompileType(buildArgs []string) (string, []string) {
	compileType := "full"
	moduleTargets := []string{}

	if len(buildArgs) == 0 {
		log.Printf("No build arguments provided, assuming full build")
		return compileType, moduleTargets
	}

	if len(buildArgs) == 1 {
		envCheckCommands := map[string]bool{
			"nothing": true, "showcommands": true, "dumpvars": true,
		}

		if envCheckCommands[buildArgs[0]] {
			fmt.Printf("Environment check command detected: %s\n", buildArgs[0])
			compileType = "env_check"
			return compileType, moduleTargets
		}
	}

	// Check if there are targets in MODULES-IN-xxx form
	for _, arg := range buildArgs {
		if strings.HasPrefix(arg, "MODULES-IN-") {
			compileType = "module"
			//  Convert MODULES-IN-foo-bar to directory path foo/bar
			dirPath := strings.TrimPrefix(arg, "MODULES-IN-")
			dirPath = strings.ReplaceAll(dirPath, "-", "/")
			moduleTargets = []string{dirPath}
			log.Printf("Module directory build detected: %v", moduleTargets)
			return compileType, moduleTargets
		}
	}

	// Check if contains mm or mmm command
	for i, arg := range buildArgs {
		switch arg {
		case "mm":
			compileType = "module"
			pwd, _ := os.Getwd()
			BuildTop := os.Getenv("ANDROID_BUILD_TOP")

			// Get current directory as module target
			if BuildTop != "" && strings.HasPrefix(pwd, BuildTop) {
				rel, _ := filepath.Rel(BuildTop, pwd)
				if rel != "." {
					moduleTargets = []string{rel}
					log.Printf("mm command detected in directory: %s", rel)
				} else {
					log.Printf("mm command executed at Android root directory")
				}
			} else {
				// Try to get module info from environment variables.Dir(oneShotMakefile)
				if oneShotMakefile := os.Getenv("ONE_SHOT_MAKEFILE"); oneShotMakefile != "" {
					dir := filepath.Dir(oneShotMakefile)
					moduleTargets = []string{dir}
					log.Printf("mm command with ONE_SHOT_MAKEFILE: %s", dir)
				} else {
					moduleTargets = []string{filepath.Base(pwd)}
					log.Printf("mm command, using current directory name: %s", filepath.Base(pwd))
				}
			}

			log.Printf("mm command detected, using directory: %v", moduleTargets)
			return compileType, moduleTargets
		case "mmm":
			compileType = "module"
			// Extract module path parameters after mmm build
			if i+1 < len(buildArgs) {
				moduleTargets = buildArgs[i+1:]
				log.Printf("mmm command detected with targets: %v", moduleTargets)
			} else {
				//  mmm command but no target specified, try to get from environment variables
				modules := os.Getenv("MODULES")
				if modules != "" {
					moduleTargets = strings.Fields(modules)
					log.Printf("mmm command, using MODULES env: %v", moduleTargets)
				} else {
					// Try to infer from current directory
					pwd, _ := os.Getwd()
					moduleTargets = []string{filepath.Base(pwd)}
					log.Printf("mmm command without targets, using current dir: %s", moduleTargets[0])
				}
			}
			return compileType, moduleTargets
		}
	}

	//  Check if it's ./build.sh command
	if len(buildArgs) > 0 && (buildArgs[0] == "./build.sh" || strings.HasSuffix(buildArgs[0], "/build.sh")) {
		//  If build.sh is followed by specific modules, consider it as module build
		if len(buildArgs) >= 2 && !strings.HasPrefix(buildArgs[1], "-") {
			compileType = "module"
			moduleTargets = []string{buildArgs[1]}
			log.Printf("build.sh module build detected: %v", moduleTargets)
		} else {
			log.Printf("build.sh full build detected")
		}
		return compileType, moduleTargets
	}

	// Check if it's m command followed by single module name,e.g.: m libfoo
	if len(buildArgs) >= 1 && buildArgs[0] != "all" {
		//  Skip m/make command itself
		startIndex := 0
		if buildArgs[0] == "m" || buildArgs[0] == "make" {
			startIndex = 1
		}

		// Check if there are real module parameters (not options starting with dash)
		if len(buildArgs) > startIndex && !strings.HasPrefix(buildArgs[startIndex], "-") {
			// This might be single or multiple module buildArgs
			compileType = "module"
			moduleTargets = buildArgs[startIndex:]
			log.Printf("Module build detected: %v", moduleTargets)
			return compileType, moduleTargets
		}
	}

	// Default is full build
	log.Printf("Full build mode detected with args: %v", buildArgs)
	return compileType, moduleTargets
}

// getAllCompilationCommands gets all compilation commands (for full build)
func getAllCompilationCommands(ctx context.Context, config WrapperConfig, tempNinjaFile string) CommandDatabase {
	commands := CommandDatabase{Commands: []CompilerCommandInfo{}}
	executable := config.NinjaTool
	fmt.Printf("Using ninja tool for compilation database: %s\n", executable)
	fmt.Printf("Getting all compilation commands from ninja file\n")

	cmd := exec.Command(executable, "-f", tempNinjaFile, "-t", "compdb")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr
	cmd.Dir = os.Getenv("ANDROID_BUILD_TOP")

	// Parse JSON output
	var compdbEntries []map[string]interface{}
	if err := json.Unmarshal(outBuf.Bytes(), &compdbEntries); err != nil {
		fmt.Printf("Failed to parse compilation database JSON: %v\n", err)
		return commands
	}

	// Convert to CommandDatabase form
	for _, entry := range compdbEntries {
		cmdInfo := parseCompdbEntry(entry, cmd.Dir)
		if cmdInfo.CompilerType != "" && len(cmdInfo.InputFiles) > 0 {
			commands.Commands = append(commands.Commands, cmdInfo)
		}
	}

	return commands
}

// detectModuleTargets detects module targets from current environment
func detectModuleTargets() []string {
	var moduleTargets []string

	// Try to get from ONE_SHOT_MAKEFILE environment variable
	oneShotMakefile := os.Getenv("ONE_SHOT_MAKEFILE")
	BuildTop := os.Getenv("ANDROID_BUILD_TOP")
	if oneShotMakefile != "" {
		dir := filepath.Dir(oneShotMakefile)
		moduleTargets = append(moduleTargets, dir)
		log.Printf("Module target detected from ONE_SHOT_MAKEFILE: %s", dir)
	}

	// Try to get from MODULES environment variable
	modules := os.Getenv("MODULES")
	if modules != "" {
		modulesList := strings.Fields(modules)
		moduleTargets = append(moduleTargets, modulesList...)
		log.Printf("Module targets detected from MODULES env: %v", modulesList)
	}

	//  If none of the above, try using current directorywd()
	if len(moduleTargets) == 0 {
		pwd, err := os.Getwd()
		if err == nil {
			// Try to get path relative to source root
			if BuildTop != "" && strings.HasPrefix(pwd, BuildTop) {
				rel, err := filepath.Rel(BuildTop, pwd)
				if err == nil && rel != "." {
					moduleTargets = append(moduleTargets, rel)
					log.Printf("Using current directory as module target: %s", rel)
				}
			}

			// If still nothing, use current directory name
			if len(moduleTargets) == 0 {
				moduleTargets = append(moduleTargets, filepath.Base(pwd))
				log.Printf("Using current directory name as module target: %s", filepath.Base(pwd))
			}
		}
	}

	return moduleTargets
}

// expandModuleTargets expands module targets, generating possible variants
func expandModuleTargets(targets []string) []string {
	if len(targets) == 0 {
		return targets
	}

	expanded := make([]string, 0, len(targets)*2)

	for _, target := range targets {
		expanded = append(expanded, target)

		// Directory form replaced with underscore form (system/core/init -> system_core_init)
		if strings.Contains(target, "/") {
			expanded = append(expanded, strings.ReplaceAll(target, "/", "_"))
		}

		baseName := filepath.Base(target)
		if baseName != target {
			expanded = append(expanded, baseName)
		}

		// lib prefix handling
		if strings.HasPrefix(baseName, "lib") {
			noLib := strings.TrimPrefix(baseName, "lib")
			expanded = append(expanded, noLib)
		} else {
			withLib := "lib" + baseName
			expanded = append(expanded, withLib)
		}
	}

	return expanded
}

// findNinjaTargetsByFuzzyMatch finds ninja targets by fuzzy matchingng for module targets
func findNinjaTargetsByFuzzyMatch(ctx context.Context, config WrapperConfig, ninjaFile string, moduleTargets []string) []string {
	fmt.Printf("Trying fuzzy matching for module targets\n")

	// Create fuzzy matching patterns from last path part
	var fuzzyPatterns []string
	for _, target := range moduleTargets {
		// Extract last part of paths
		parts := strings.Split(target, "/")
		last := parts[len(parts)-1]

		// Create several fuzzy patterns
		fuzzyPatterns = append(fuzzyPatterns, last)

		// Handle lib prefix
		if strings.HasPrefix(last, "lib") {
			noPrefix := strings.TrimPrefix(last, "lib")
			fuzzyPatterns = append(fuzzyPatterns, noPrefix)
		} else {
			withPrefix := "lib" + last
			fuzzyPatterns = append(fuzzyPatterns, withPrefix)
		}

		// Add known Android module type prefix
		moduleTypes := []string{"SHARED_LIBRARIES", "STATIC_LIBRARIES", "EXECUTABLES", "APPS"}
		for _, moduleType := range moduleTypes {
			fuzzyPatterns = append(fuzzyPatterns, moduleType+"_"+last)
		}
	}

	fmt.Printf("Fuzzy patterns: %v\n", fuzzyPatterns)

	allTargets := getNinjaTargets(ctx, config, ninjaFile)

	//  Find matching targets
	var matchedTargets []string
	for _, target := range allTargets {
		targetLower := strings.ToLower(target)

		for _, pattern := range fuzzyPatterns {
			patternLower := strings.ToLower(pattern)

			if strings.Contains(targetLower, patternLower) {
				matchedTargets = append(matchedTargets, target)
				break
			}
		}
	}

	fmt.Printf("Found %d targets by fuzzy matching\n", len(matchedTargets))
	return matchedTargets
}

func getCompilationDatabase(ctx context.Context, config WrapperConfig, ninjaFile string, targets []string) CommandDatabase {
	commands := CommandDatabase{Commands: []CompilerCommandInfo{}}
	executable := config.NinjaTool
	ninjaDir := filepath.Dir(ninjaFile)
	BuildTop := os.Getenv("ANDROID_BUILD_TOP")

	// If no targets specified, get all compilation commands
	if len(targets) == 0 {
		fmt.Println("Getting all compilation commands (no targets specified)")
		args := []string{"-f", ninjaFile, "-t", "compdb"}
		cmd := exec.Command(executable, args...)
		cmd.Dir = BuildTop

		var outBuf bytes.Buffer
		cmd.Stdout = &outBuf

		if err := cmd.Run(); err != nil {
			fmt.Println("Failed to get compilation database:", err)
			return commands
		}

		// Parse JSON output
		var compdbEntries []map[string]interface{}
		if err := json.Unmarshal(outBuf.Bytes(), &compdbEntries); err != nil {
			fmt.Println("Failed to parse JSON", err)
			return commands
		}

		for _, entry := range compdbEntries {
			cmdInfo := parseCompdbEntry(entry, ninjaDir)
			if cmdInfo.CompilerType != "" && len(cmdInfo.InputFiles) > 0 {
				commands.Commands = append(commands.Commands, cmdInfo)
			}
		}
		return commands
	}

	// Process targets one by one
	fmt.Printf("Starting to get compilation commands for %d targets\n", len(targets))
	for i, target := range targets {
		fmt.Printf("Processing target %d/%d: %s\n", i+1, len(targets), target)

		args := []string{"-f", ninjaFile, "-t", "compdb-targets", target}
		cmd := exec.Command(executable, args...)
		cmd.Dir = BuildTop

		var outBuf bytes.Buffer
		cmd.Stdout = &outBuf

		if err := cmd.Run(); err != nil {
			fmt.Printf("Failed to get compilation commands for target %s: %v\n", target, err)
			continue
		}

		fmt.Printf("Raw output length: %d bytes\n", outBuf.Len())
		if outBuf.Len() < 1000 {
			fmt.Printf("Raw output content: %s\n", outBuf.String())
		}
		var compdbEntries []map[string]interface{}
		if err := json.Unmarshal(outBuf.Bytes(), &compdbEntries); err != nil {
			fmt.Printf("Failed to parse JSON for target %s: %v\n", target, err)
			continue
		}

		for _, entry := range compdbEntries {
			cmdInfo := parseCompdbEntry(entry, ninjaDir)
			if cmdInfo.CompilerType != "" && len(cmdInfo.InputFiles) > 0 {
				//  Check if command already exists
				if !isCommandExists(commands.Commands, cmdInfo) {
					commands.Commands = append(commands.Commands, cmdInfo)
				}
			}
		}
	}

	fmt.Printf("Successfully got %d compilation commands for %d targets\n", len(targets), len(commands.Commands))
	return commands
}

func isCommandExists(commands []CompilerCommandInfo, newCmd CompilerCommandInfo) bool {
	for _, cmd := range commands {
		if cmd.Command == newCmd.Command &&
			cmd.OutputFile == newCmd.OutputFile &&
			strings.Join(cmd.InputFiles, ",") == strings.Join(newCmd.InputFiles, ",") {
			return true
		}
	}
	return false
}

// splitCommandLine splits command line string into argument list, handling quotes
func splitCommandLine(cmdLine string) []string {
	var args []string
	var current string
	var inQuote bool
	var quoteChar rune

	for _, r := range cmdLine {
		if r == '"' || r == '\'' {
			if inQuote && r == quoteChar {
				inQuote = false
			} else if !inQuote {
				inQuote = true
				quoteChar = r
			} else {
				current += string(r)
			}
		} else if r == ' ' && !inQuote {
			if current != "" {
				args = append(args, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}

	if current != "" {
		args = append(args, current)
	}

	return args
}

// determineCompilerTypeFromCommand determines compiler type from command string
func determineCompilerTypeFromCommand(command string) string {
	compilerType := ""

	parts := strings.Fields(command)
	if len(parts) > 0 {
		if strings.HasPrefix(parts[0], "PWD=") {
			compilerType = parts[1]
		} else {
			compilerType = parts[0]
		}
	} else {
		return compilerType
	}

	fmt.Printf("compilerType: %s\n", compilerType)

	if strings.Contains(compilerType, "clang++") {
		return "clang++"
	} else if strings.Contains(compilerType, "clang") && !strings.Contains(compilerType, "++") {
		return "clang"
	} else if strings.Contains(compilerType, "g++") {
		return "g++"
	} else if strings.Contains(compilerType, "gcc") && !strings.Contains(compilerType, "++") {
		return "gcc"
	} else if strings.Contains(compilerType, "javac") {
		return "javac"
	} else if strings.Contains(compilerType, "kotlinc") {
		return "kotlinc"
	} else if strings.Contains(compilerType, "r8") || strings.Contains(compilerType, "d8") {
		return "android-dex"
	}

	return compilerType
}

// parseAdditionalCommandInfo parses additional information from command string
func parseAdditionalCommandInfo(info *CompilerCommandInfo) {
	args := splitCommandLine(info.Command)

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Handle include path
		if strings.HasPrefix(arg, "-I") {
			if len(arg) > 2 {
				info.Includes = append(info.Includes, arg[2:])
			} else if i+1 < len(args) {
				info.Includes = append(info.Includes, args[i+1])
				i++
			}
		}

		// Handle macro definitions
		if strings.HasPrefix(arg, "-D") {
			if len(arg) > 2 {
				info.Defines = append(info.Defines, arg[2:])
			} else if i+1 < len(args) {
				info.Defines = append(info.Defines, args[i+1])
				i++
			}
		}

		// Handle other compilation flags
		if strings.HasPrefix(arg, "-") && arg != "-o" && !strings.HasPrefix(arg, "-I") && !strings.HasPrefix(arg, "-D") {
			info.Flags = append(info.Flags, arg)
		}
	}
}

// parseCompdbEntry parses compilation database entry to CompilerCommandInfo
func parseCompdbEntry(entry map[string]interface{}, defaultWorkingDir string) CompilerCommandInfo {
	info := CompilerCommandInfo{
		WorkingDir: defaultWorkingDir, // 设置默认工作目录
	}

	//  Get basic fields
	if cmd, ok := entry["command"].(string); ok {
		info.Command = cmd
	}

	if dir, ok := entry["directory"].(string); ok && dir != "" {
		info.WorkingDir = dir // 使用条目中的目录
	}

	if file, ok := entry["file"].(string); ok {
		info.InputFiles = []string{file}
	}

	if files, ok := entry["input_files"].([]interface{}); ok {
		info.InputFiles = []string{}
		for _, f := range files {
			if fileStr, ok := f.(string); ok {
				info.InputFiles = append(info.InputFiles, fileStr)
			}
		}
	} else if files, ok := entry["sources"].([]interface{}); ok {
		// Some ninja versions might use sources field
		info.InputFiles = []string{}
		for _, f := range files {
			if fileStr, ok := f.(string); ok {
				info.InputFiles = append(info.InputFiles, fileStr)
			}
		}
	}

	if output, ok := entry["output"].(string); ok {
		info.OutputFile = output
	} else if output, ok := entry["target"].(string); ok {
		// Some ninja versions might use target field as output
		info.OutputFile = output
	}

	// Determine compiler type
	info.CompilerType = determineCompilerTypeFromCommand(info.Command)

	// Parse command line for more information
	if info.Command != "" {
		parseAdditionalCommandInfo(&info)
	}

	// Try to guess module name from output path
	if info.OutputFile != "" {
		info.Module = extractModuleNameFromPath(info.OutputFile)
	}

	return info
}

// extractModuleNameFromPath extracts Android module name from path
func extractModuleNameFromPath(path string) string {
	// Common module path patterns in Android build system
	patterns := []*regexp.Regexp{
		// e.g: out/target/product/XXX/obj/SHARED_LIBRARIES/libxxx_intermediates/
		regexp.MustCompile(`/obj/([A-Z_]+)/([^/]+)_intermediates/`),
		// e.g: out/soong/.intermediates/path/to/module/variant/
		regexp.MustCompile(`/.intermediates/([^/]+/)*([^/]+)/[^/]+/`),
		// Simple extraction of last meaningful directory name
		regexp.MustCompile(`([^/]+)/_intermediates/`),
	}

	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(path)
		if len(matches) >= 3 {
			return matches[2]
		} else if len(matches) >= 2 {
			return matches[1]
		}
	}

	// If above patterns don't match, try simpler approach
	// Exclude some common non-module directory name
	excludeDirs := map[string]bool{
		"out": true, "intermediates": true, "obj": true,
		"EXECUTABLES": true, "SHARED_LIBRARIES": true, "STATIC_LIBRARIES": true,
		"APPS": true, "include": true, "lib": true, "bin": true,
	}

	parts := strings.Split(path, "/")
	for i := len(parts) - 2; i >= 0; i-- {
		part := parts[i]
		// Skip empty parts and excluded directories
		if part != "" && !excludeDirs[part] && !strings.HasPrefix(part, ".") {
			// Remove _intermediates suffix
			return strings.TrimSuffix(part, "_intermediates")
		}
	}

	return ""
}

// getNinjaTargets updated with proper cleanup
func getNinjaTargets(ctx context.Context, config WrapperConfig, ninjaFile string) []string {
	executable := config.NinjaTool
	// Run ninja -t targets command
	cmd := exec.Command(executable, "-f", ninjaFile, "-t", "targets")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Dir = os.Getenv("ANDROID_BUILD_TOP")

	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to get ninja targets: %v\n", err)
		fmt.Printf("Falling back to direct extraction from ninja file\n")
		return nil
	}

	// Parse output
	return parseNinjaTargetsOutput(&outBuf)
}

// parseNinjaTargetsOutput helper function
func parseNinjaTargetsOutput(outBuf *bytes.Buffer) []string {
	var targets []string
	scanner := bufio.NewScanner(outBuf)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) > 0 {
			target := strings.TrimSpace(parts[0])
			if target != "" {
				targets = append(targets, target)
			}
		}
	}

	fmt.Printf("Found %d targets\n", len(targets))
	return targets
}

func writeCompileCommands(outputDir string, commands CommandDatabase) error {
	BuildTop := os.Getenv("ANDROID_BUILD_TOP")
	CompileCommandsFile := "compile_commands.json"

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

	jsonData, err := json.MarshalIndent(commands, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON encoding failed: %v", err)
	}

	tempFile := filepath.Join(outputDir, ".compile_commands.tmp")
	if err := os.WriteFile(tempFile, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write temporary file: %v", err)
	}

	finalPath := filepath.Join(outputDir, CompileCommandsFile)
	if err := os.Rename(tempFile, finalPath); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("failed to rename file: %v", err)
	}

	fmt.Printf("Running proxy: proxy -w %s -c %s\n", BuildTop, CompileCommandsFile)
	cmd := exec.Command("proxy", "-w", BuildTop, "-c", CompileCommandsFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run proxy command: %v", err)
	}

	return nil
}

func createTempNinjaFile(ninjaFile string) (string, error) {
	// Create a temporary ninja file with pool definitions and include the original ninja file
	tmpNinjaFile := ninjaFile + ".tmp_commands"
	// Extract pool definitions from combined.ninja file, or create default pool definitions
	poolDefs := `
pool highmem_pool
  depth = 1`
	combinedNinjaContent := poolDefs + "\nsubninja " + ninjaFile + "\n"
	if err := os.WriteFile(tmpNinjaFile, []byte(combinedNinjaContent), 0666); err != nil {
		fmt.Printf("Failed to create temporary ninja file: %v\n", err)
		return "", fmt.Errorf("error creating temporary ninja file: %s", tmpNinjaFile)
	}

	return tmpNinjaFile, nil
}

// findTargetsByModulePath finds targets by fuzzy matching module path
func findTargetsByModulePath(allTargets []string, module string) []string {
	var matchedTargets []string
	moduleParts := strings.Split(module, "/")

	for _, target := range allTargets {
		// Matching rule 1: Target path contains module path
		if strings.Contains(target, module) {
			matchedTargets = append(matchedTargets, target)
			continue
		}

		// Matching rule 2: Target path contains all parts of module path
		allPartsMatched := true
		for _, part := range moduleParts {
			if !strings.Contains(target, part) {
				allPartsMatched = false
				break
			}
		}
		if allPartsMatched {
			matchedTargets = append(matchedTargets, target)
			continue
		}

		// Matching rule 3: Target path contains variants of module name
		moduleName := filepath.Base(module)
		if strings.Contains(target, moduleName) {
			matchedTargets = append(matchedTargets, target)
		}
	}

	return matchedTargets
}

// getRelevantTargets gets targets related to the module
func getRelevantTargets(ctx context.Context, config WrapperConfig, ninjaFile string, module string) []string {
	allTargets := getNinjaTargets(ctx, config, ninjaFile)
	fmt.Printf("Got %d targets\n", len(allTargets))

	matchedTargets := findTargetsByModulePath(allTargets, module)
	fmt.Printf("Matched %d relevant targets\n", len(matchedTargets))

	// Further filter build targets (exclude .tidy and other auxiliary targets)
	var buildTargets []string
	for _, target := range matchedTargets {
		if !strings.Contains(target, ".tidy") &&
			!strings.Contains(target, ".lint") &&
			!strings.Contains(target, ".analyze") {
			buildTargets = append(buildTargets, target)
		}
	}

	fmt.Printf("After filtering, got %d build targets\n", len(buildTargets))
	return buildTargets
}
