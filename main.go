package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containrrr/shoutrrr"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

const defaultStateFile = "state.yml"

type Config struct {
	AccessToken   string         `yaml:"access_token,omitempty"`
	Interval      int            `yaml:"interval"`
	StateFile     string         `yaml:"state_file,omitempty"`
	Repositories  []Repository   `yaml:"repositories"`
	Notifications []Notification `yaml:"notifications"`
}

type Repository struct {
	Slug string `yaml:"slug"`
}

type Notification struct {
	RawURL string `yaml:"url"`
}

type State struct {
	Releases map[string]string `yaml:"releases"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	configFile := getConfigFile()

	config, err := loadConfig(configFile)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return
	}

	applyEnvOverrides(config)

	if len(config.Repositories) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "No repositories configured (set repositories in config.yml or GRM_REPOSITORIES)")
		return
	}

	stateFile := resolveStateFile(config)
	state, err := loadState(stateFile)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading state: %v\n", err)
		return
	}

	client := createGithubClient(ctx, *config)

	fmt.Println("Starting initial repository check...")
	runCheck(ctx, config, client, state, stateFile)

	if config.Interval == 0 {
		fmt.Println("Running in one-shot mode (no interval)")
		return
	}

	interval := time.Duration(config.Interval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	fmt.Printf("Running in daemon mode, checking every %v\n", interval)

	for {
		select {
		case <-ticker.C:
			runCheck(ctx, config, client, state, stateFile)
		case <-sigChan:
			fmt.Println("\nReceived shutdown signal, saving state and exiting...")
			if err := saveState(stateFile, state, config); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func runCheck(ctx context.Context, config *Config, client *github.Client, state *State, stateFile string) {
	fmt.Printf("[%s] Checking %d repositories...\n", time.Now().Format(time.RFC3339), len(config.Repositories))

	err := checkRepositories(ctx, config, client, state)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error checking repositories: %v\n", err)
	}

	if err := saveState(stateFile, state, config); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error writing state: %v\n", err)
	}

	fmt.Println("Check completed")
}

func getConfigFile() string {
	if envConfig := os.Getenv("GITHUB_RELEASE_MONITOR_CONFIG"); envConfig != "" {
		return envConfig
	}
	return "config.yml"
}

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("Config file %s not found, relying on environment variables\n", filename)
			return &Config{}, nil
		}
		return nil, err
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func applyEnvOverrides(config *Config) {
	if v := os.Getenv("GRM_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			config.Interval = n
		} else {
			_, _ = fmt.Fprintf(os.Stderr, "Invalid GRM_INTERVAL=%q, ignoring\n", v)
		}
	}
	if v := os.Getenv("GRM_STATE_FILE"); v != "" {
		config.StateFile = v
	}
	if v := os.Getenv("GRM_REPOSITORIES"); v != "" {
		repos := make([]Repository, 0)
		for _, slug := range strings.Split(v, ",") {
			slug = strings.TrimSpace(slug)
			if slug == "" {
				continue
			}
			repos = append(repos, Repository{Slug: slug})
		}
		config.Repositories = repos
	}
	if v := os.Getenv("GRM_NOTIFICATIONS"); v != "" {
		notifs := make([]Notification, 0)
		for _, url := range strings.Split(v, ",") {
			url = strings.TrimSpace(url)
			if url == "" {
				continue
			}
			notifs = append(notifs, Notification{RawURL: url})
		}
		config.Notifications = notifs
	}
}

func resolveStateFile(config *Config) string {
	if config.StateFile != "" {
		return config.StateFile
	}
	return defaultStateFile
}

func loadState(filename string) (*State, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &State{Releases: map[string]string{}}, nil
		}
		return nil, err
	}
	var state State
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Releases == nil {
		state.Releases = map[string]string{}
	}
	return &state, nil
}

func saveState(filename string, state *State, config *Config) error {
	pruneState(state, config)
	data, err := yaml.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func pruneState(state *State, config *Config) {
	wanted := make(map[string]struct{}, len(config.Repositories))
	for _, r := range config.Repositories {
		wanted[r.Slug] = struct{}{}
	}
	for slug := range state.Releases {
		if _, ok := wanted[slug]; !ok {
			delete(state.Releases, slug)
		}
	}
}

func createGithubClient(ctx context.Context, config Config) *github.Client {
	accessToken := config.AccessToken
	if accessToken == "" {
		accessToken = os.Getenv("GITHUB_TOKEN")
		if accessToken != "" {
			fmt.Println("Using GitHub access token from environment variable")
		}
	}
	if accessToken != "" {
		if config.AccessToken != "" {
			fmt.Println("Using provided GitHub access token for authentication")
		}
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: accessToken},
		)
		tc := oauth2.NewClient(ctx, ts)
		return github.NewClient(tc)
	}
	return github.NewClient(nil)
}

func parseSlug(slug string) (owner string, repo string, err error) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid slug format: %s", slug)
	}
	return parts[0], parts[1], nil
}

func checkRepositories(ctx context.Context, config *Config, client *github.Client, state *State) error {
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := range config.Repositories {
		wg.Add(1)
		go func(repo *Repository) {
			defer wg.Done()

			err := checkRepository(ctx, repo, client, state, &mu, config.Notifications)
			if err != nil {
				mu.Lock()
				_, _ = fmt.Fprintf(os.Stderr, "Error checking repository %s: %v\n", repo.Slug, err)
				mu.Unlock()
			}
		}(&config.Repositories[i])
	}

	wg.Wait()
	return nil
}

func checkRepository(ctx context.Context, repo *Repository, client *github.Client, state *State, mu *sync.Mutex, notifications []Notification) error {
	owner, repoName, err := parseSlug(repo.Slug)
	if err != nil {
		return err
	}

	tagName, err := getLatestReleaseTag(ctx, client, owner, repoName)
	if err != nil {
		return fmt.Errorf("error fetching release for %s: %w", repo.Slug, err)
	}

	mu.Lock()
	previous := state.Releases[repo.Slug]
	changed := previous != tagName
	if changed {
		state.Releases[repo.Slug] = tagName
	}
	mu.Unlock()

	if changed {
		notifyNewRelease(repo.Slug, tagName, notifications)
	}

	return nil
}

func getLatestReleaseTag(ctx context.Context, client *github.Client, owner, repo string) (string, error) {
	release, _, err := client.Repositories.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return release.GetTagName(), nil
}

func notifyNewRelease(slug, tagName string, notifications []Notification) {
	message := fmt.Sprintf("New release for %s: %s", slug, tagName)
	fmt.Println(message)

	for _, notification := range notifications {
		formattedMessage := formatNotificationMessage(notification.RawURL, slug, tagName, message)

		err := shoutrrr.Send(notification.RawURL, formattedMessage)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Error sending notification to %s: %v\n", notification.RawURL, err)
		}
	}
}

func formatNotificationMessage(url, slug, tagName, defaultMessage string) string {
	if strings.HasPrefix(url, "generic+powerautomate") {
		return formatTeamsPowerAutomateMessage(slug, tagName)
	}
	return defaultMessage
}

func formatTeamsPowerAutomateMessage(slug, tagName string) string {
	repoURL := fmt.Sprintf("https://github.com/%s", slug)
	releaseURL := fmt.Sprintf("https://github.com/%s/releases/tag/%s", slug, tagName)
	owner, _, _ := parseSlug(slug)
	imageURL := fmt.Sprintf("https://github.com/%s.png", owner)
	return fmt.Sprintf(`{
    "type": "message",
    "attachments": [{
        "contentType": "application/vnd.microsoft.card.adaptive",
        "content": {
            "type": "AdaptiveCard",
            "$schema": "https://adaptivecards.io/schemas/adaptive-card.json",
            "version": "1.5",
            "body": [
                {
                    "type": "ColumnSet",
                    "columns": [
                        {
                            "type": "Column",
                            "width": "auto",
                            "items": [
                                {
                                    "type": "Image",
                                    "url": "%s",
                                    "size": "Large"
                                }
                            ]
                        },
                        {
                            "type": "Column",
                            "width": "stretch",
                            "items": [
                                {
                                    "type": "TextBlock",
                                    "text": "New Release Available",
                                    "weight": "Bolder",
                                    "size": "Large"
                                },
                                {
                                    "type": "FactSet",
                                    "facts": [
                                        {
                                            "title": "Repository:",
                                            "value": "[%s](%s)"
                                        },
                                        {
                                            "title": "Version:",
                                            "value": "[%s](%s)"
                                        }
                                    ]
                                }
                            ]
                        }
                    ]
                }
            ]
        }
    }]
}`, imageURL, slug, repoURL, tagName, releaseURL)
}
