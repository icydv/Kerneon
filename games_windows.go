//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type InstalledGame struct {
	ID, Name, Provider, LaunchTarget string
}

var vdfFieldPattern = regexp.MustCompile(`(?m)"([^"]+)"\s+"([^"]*)"`)

func discoverInstalledGames() []InstalledGame {
	games := append(discoverSteamGames(), discoverEpicGames()...)
	seen := make(map[string]bool, len(games))
	result := games[:0]
	for _, game := range games {
		key := strings.ToLower(game.Provider + "\x00" + game.Name)
		if game.ID == "" || game.Name == "" || game.LaunchTarget == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, game)
	}
	sort.SliceStable(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result
}

func discoverSteamGames() []InstalledGame {
	roots := []string{filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam")}
	if data, err := os.ReadFile(filepath.Join(roots[0], "steamapps", "libraryfolders.vdf")); err == nil {
		for _, match := range vdfFieldPattern.FindAllStringSubmatch(string(data), -1) {
			if strings.EqualFold(match[1], "path") {
				roots = append(roots, strings.ReplaceAll(match[2], `\\`, `\`))
			}
		}
	}
	var games []InstalledGame
	seenRoot := make(map[string]bool)
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || seenRoot[strings.ToLower(root)] {
			continue
		}
		seenRoot[strings.ToLower(root)] = true
		manifests, _ := filepath.Glob(filepath.Join(root, "steamapps", "appmanifest_*.acf"))
		for _, manifest := range manifests {
			data, err := os.ReadFile(manifest)
			if err != nil {
				continue
			}
			fields := make(map[string]string)
			for _, match := range vdfFieldPattern.FindAllStringSubmatch(string(data), -1) {
				fields[strings.ToLower(match[1])] = match[2]
			}
			appid, name := strings.TrimSpace(fields["appid"]), strings.TrimSpace(fields["name"])
			lower := strings.ToLower(name)
			if appid == "" || name == "" || strings.Contains(lower, "redistributable") || strings.Contains(lower, "steamworks common") {
				continue
			}
			games = append(games, InstalledGame{ID: "steam:" + appid, Name: name, Provider: "Steam", LaunchTarget: "steam://rungameid/" + appid})
		}
	}
	return games
}

func discoverEpicGames() []InstalledGame {
	programData := os.Getenv("ProgramData")
	manifests, _ := filepath.Glob(filepath.Join(programData, "Epic", "EpicGamesLauncher", "Data", "Manifests", "*.item"))
	games := make([]InstalledGame, 0, len(manifests))
	for _, manifest := range manifests {
		var item struct {
			DisplayName string `json:"DisplayName"`
			AppName     string `json:"AppName"`
			Launchable  bool   `json:"bIsIncompleteInstall"`
		}
		data, err := os.ReadFile(manifest)
		if err != nil || json.Unmarshal(data, &item) != nil || item.DisplayName == "" || item.AppName == "" || item.Launchable {
			continue
		}
		games = append(games, InstalledGame{ID: "epic:" + item.AppName, Name: item.DisplayName, Provider: "Epic", LaunchTarget: "com.epicgames.launcher://apps/" + item.AppName + "?action=launch&silent=true"})
	}
	return games
}
