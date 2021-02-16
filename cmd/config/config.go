package config

import (
	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v2"
)

// Environ returns the settings from the environment.
func Environ() (*Config, error) {
	cfg := Config{}
	err := envconfig.Process("", &cfg)
	defaultDB(&cfg)

	return &cfg, err
}

func defaultDB(c *Config) {
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite3"
	}
	if c.Database.Config == "" {
		c.Database.Config = "gimletd.sqlite"
	}
}

// String returns the configuration in string format.
func (c *Config) String() string {
	out, _ := yaml.Marshal(c)
	return string(out)
}

type Config struct {
	Debug                   bool `envconfig:"DEBUG"`
	Logging                 Logging
	Host                    string `envconfig:"HOST"`
	Database                Database
	GitopsRepoUrl           string `envconfig:"GITOPS_REPO_SSH_ADDRESS"`
	GitopsRepoDeployKeyPath string `envconfig:"GITOPS_REPO_DEPLOY_KEY_PATH"`
	Notifications           Notifications
}

type Database struct {
	Driver string `envconfig:"DATABASE_DRIVER"`
	Config string `envconfig:"DATABASE_CONFIG"`
}

// Logging provides the logging configuration.
type Logging struct {
	Debug  bool `envconfig:"DEBUG"`
	Trace  bool `envconfig:"TRACE"`
	Color  bool `envconfig:"LOGS_COLOR"`
	Pretty bool `envconfig:"LOGS_PRETTY"`
	Text   bool `envconfig:"LOGS_TEXT"`
}

type Notifications struct {
	Provider       string `envconfig:"NOTIFICATIONS_PROVIDER"`
	Token          string `envconfig:"NOTIFICATIONS_TOKEN"`
	DefaultChannel string `envconfig:"NOTIFICATIONS_DEFAULT_CHANNEL"`
	ChannelMapping string `envconfig:"NOTIFICATIONS_CHANNEL_MAPPING"`
}
