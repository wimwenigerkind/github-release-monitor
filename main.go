package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containrrr/shoutrrr"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

type Config struct {
	AccessToken   string         `yaml:"access_token,omitempty"`
	Interval      int            `yaml:"interval"`
	Repositories  []Repository   `yaml:"repositories"`
	Notifications []Notification `yaml:"notifications"`
}

type Repository struct {
	Slug              string `yaml:"slug"`
	CurrentReleaseTag string `yaml:"current_release_tag"`
}

type Notification struct {
	RawURL string `yaml:"url"`
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

	client := createGithubClient(ctx, *config)

	fmt.Println("Starting initial repository check...")
	runCheck(ctx, config, client, configFile)

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
			runCheck(ctx, config, client, configFile)
		case <-sigChan:
			fmt.Println("\nReceived shutdown signal, saving config and exiting...")
			if err := saveConfig(configFile, config); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func runCheck(ctx context.Context, config *Config, client *github.Client, configFile string) {
	fmt.Printf("[%s] Checking %d repositories...\n", time.Now().Format(time.RFC3339), len(config.Repositories))

	err := checkRepositories(ctx, config, client)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error checking repositories: %v\n", err)
	}

	err = saveConfig(configFile, config)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
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
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func saveConfig(filename string, config *Config) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
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

func checkRepositories(ctx context.Context, config *Config, client *github.Client) error {
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := range config.Repositories {
		wg.Add(1)
		go func(repo *Repository) {
			defer wg.Done()

			err := checkRepository(ctx, repo, client, config.Notifications)
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

func checkRepository(ctx context.Context, repo *Repository, client *github.Client, notifications []Notification) error {
	owner, repoName, err := parseSlug(repo.Slug)
	if err != nil {
		return err
	}

	tagName, err := getLatestReleaseTag(ctx, client, owner, repoName)
	if err != nil {
		return fmt.Errorf("error fetching release for %s: %w", repo.Slug, err)
	}

	updateReleaseTag(repo, tagName, notifications)

	return nil
}

func getLatestReleaseTag(ctx context.Context, client *github.Client, owner, repo string) (string, error) {
	release, _, err := client.Repositories.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return release.GetTagName(), nil
}

func updateReleaseTag(repo *Repository, tagName string, notifications []Notification) {
	if repo.CurrentReleaseTag != tagName {
		repo.CurrentReleaseTag = tagName
		notifyNewRelease(repo.Slug, tagName, notifications)
	}
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
	return fmt.Sprintf(`{
		"type": "message",
		"attachments": [{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": {
				"type": "AdaptiveCard",
				"version": "1.2",
				"body": [{
					"type": "TextBlock",
					"text": "New Release Available",
					"weight": "bolder",
					"size": "large"
				},{
					"type": "FactSet",
					"facts": [{
						"title": "Repository:",
						"value": "%s"
					},{
						"title": "Version:",
						"value": "%s"
					}]
				}]
			}
		}]
	}`, slug, tagName)
}
