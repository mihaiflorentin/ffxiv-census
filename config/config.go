package config

import (
	"bufio"
	"bytes"
	"embed"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

//go:embed config.toml
var configFS embed.FS

type Config struct {
	App       AppConfig        `mapstructure:"app"`
	Logging   *LoggingConfig   `mapstructure:"logging"`
	HTTP      HTTPConfig       `mapstructure:"http"`
	Auth      *AuthConfig      `mapstructure:"auth"`
	Metrics   *MetricsConfig   `mapstructure:"metrics"`
	Postgres  *PostgresConfig  `mapstructure:"postgres"`
	RabbitMQ  *RabbitMQConfig  `mapstructure:"rabbitmq"`
	Queue     *QueueConfig     `mapstructure:"queue"`
	Writer    *WriterConfig    `mapstructure:"writer"`
	Lodestone *LodestoneConfig `mapstructure:"lodestone"`
	Census    *CensusConfig    `mapstructure:"census"`
	Proxy     *ProxyConfig     `mapstructure:"proxy"`
}

// QueueConfig holds the broker-agnostic delivery attempt budget. maxAttempts
// bounds how many times a message may fail before it parks permanently in
// its failed queue.
type QueueConfig struct {
	MaxAttempts int `mapstructure:"max_attempts"`
}

// WriterConfig tunes the per-pod batching writer that stores finished census
// results. A batch flushes when BatchSize results are pending or FlushInterval
// has passed since its first result, whichever comes first.
type WriterConfig struct {
	BatchSize     int           `mapstructure:"batch_size"`
	FlushInterval time.Duration `mapstructure:"flush_interval"`
}

type AppConfig struct {
	Name    string `mapstructure:"name"`
	Env     string `mapstructure:"env"`
	BaseURL string `mapstructure:"base_url"`
}

type LoggingConfig struct {
	Default       string `mapstructure:"default"`
	ServerDefault string `mapstructure:"server_default"`
	Level         string `mapstructure:"level"`
}

type HTTPConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	ReadTimeout  string `mapstructure:"read_timeout"`
	WriteTimeout string `mapstructure:"write_timeout"`
	IdleTimeout  string `mapstructure:"idle_timeout"`
}

type AuthConfig struct {
	Enabled bool     `mapstructure:"enabled"`
	Token   string   `mapstructure:"token"`
	Routes  []string `mapstructure:"routes"`
	Users   []string `mapstructure:"users"`
}
type MetricsConfig struct {
	Endpoint string `mapstructure:"endpoint"`
	Prefix   string `mapstructure:"prefix"`
}
type PostgresConfig struct {
	DSN          string `mapstructure:"dsn"`
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	Database     string `mapstructure:"database"`
	SSLMode      string `mapstructure:"sslmode"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
}

func (p *PostgresConfig) GetDSN() string {
	if p.DSN != "" {
		return p.DSN
	}
	sslmode := p.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	host := p.Host
	if host == "" {
		host = "localhost"
	}
	port := p.Port
	if port == 0 {
		port = 5432
	}
	user := p.User
	if user == "" {
		user = "census"
	}
	dbname := p.Database
	if dbname == "" {
		dbname = "ffxiv_census"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s", user, p.Password, host, port, dbname, sslmode)
}

type RabbitMQConfig struct {
	URL      string `mapstructure:"url"`
	Host     string `mapstructure:"host"`
	AMQPPort int    `mapstructure:"amqp_port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Vhost    string `mapstructure:"vhost"`
}

func (r *RabbitMQConfig) GetURL() string {
	if r.URL != "" {
		return r.URL
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	port := r.AMQPPort
	if port == 0 {
		port = 5672
	}
	user := r.User
	if user == "" {
		user = "guest"
	}
	vhost := r.Vhost
	if vhost == "" {
		vhost = "/"
	}
	return fmt.Sprintf("amqp://%s:%s@%s:%d/%s", user, r.Password, host, port, vhost)
}

type LodestoneConfig struct {
	RateLimit  float64 `mapstructure:"rate_limit"`
	MaxRetries int     `mapstructure:"max_retries"`
}
type ExpansionConfig struct {
	Name          string `mapstructure:"name"`
	Version       string `mapstructure:"version"`
	FinalQuest    string `mapstructure:"final_quest"`
	Icon          string `mapstructure:"icon"`
	LevelCap      uint32 `mapstructure:"level_cap"`
	AchievementID uint32 `mapstructure:"achievement_id"`
}

type CensusConfig struct {
	ActivityWindowDays int `mapstructure:"activity_window_days"`
	// PrivateRecheckDays is how many days a character found private is left
	// alone before its achievement census runs again.
	PrivateRecheckDays int               `mapstructure:"private_recheck_days"`
	MaxLevel           uint32            `mapstructure:"max_level"`
	Expansions         []ExpansionConfig `mapstructure:"expansions"`
	UIStats            *UIStatsConfig    `mapstructure:"ui_stats"`
}

type UIStatsConfig struct {
	CacheTTL       string `mapstructure:"cache_ttl"`
	StaleWarning   string `mapstructure:"stale_warning"`
	RefreshTimeout string `mapstructure:"refresh_timeout"`
}

type ProxyConfig struct {
	TestURL            string              `mapstructure:"test_url"`
	TestTimeout        time.Duration       `mapstructure:"test_timeout"`
	DeadThresholdDays  int                 `mapstructure:"dead_threshold_days"`
	FailCountThreshold int                 `mapstructure:"fail_count_threshold"`
	Scan               ProxyScanConfig     `mapstructure:"scan"`
	Providers          ProxyProviderConfig `mapstructure:"providers"`
	Consumer           ProxyConsumerConfig `mapstructure:"consumer"`
}

// ProxyScanConfig is the validated scheduler configuration: cohort
// intervals, capacity weights, lease/repeat floors and the guard control
// cadence. All durations are Viper-decoded from duration strings.
type ProxyScanConfig struct {
	VerificationInterval time.Duration `mapstructure:"verification_interval"`
	FreshnessTTL         time.Duration `mapstructure:"freshness_ttl"`
	RecoveryBase         time.Duration `mapstructure:"recovery_base"`
	RecoveryCap          time.Duration `mapstructure:"recovery_cap"`
	RecoveryHorizon      time.Duration `mapstructure:"recovery_horizon"`
	WeightVerification   int           `mapstructure:"weight_verification"`
	WeightRecovery       int           `mapstructure:"weight_recovery"`
	WeightBackground     int           `mapstructure:"weight_background"`
	LeaseDuration        time.Duration `mapstructure:"lease_duration"`
	MinRepeatInterval    time.Duration `mapstructure:"min_repeat_interval"`
	InconclusiveRetry    time.Duration `mapstructure:"inconclusive_retry"`
	ControlInterval      time.Duration `mapstructure:"control_interval"`
}

type ProxyConsumerConfig struct {
	LockTTL            string        `mapstructure:"lock_ttl"`
	LodestoneRateLimit float64       `mapstructure:"lodestone_rate_limit"`
	RequestTimeout     string        `mapstructure:"request_timeout"`
	TestURL            string        `mapstructure:"test_url"`
	TestTimeout        time.Duration `mapstructure:"test_timeout"`
	Cooldown           time.Duration `mapstructure:"cooldown"`
}

type ProxyProviderConfig struct {
	ProxyScrape      bool   `mapstructure:"proxyscrape"`
	ProxyScrapeURL   string `mapstructure:"proxyscrape_url"`
	Geonode          bool   `mapstructure:"geonode"`
	GeonodeURL       string `mapstructure:"geonode_url"`
	PubProxy         bool   `mapstructure:"pubproxy"`
	PubProxyURL      string `mapstructure:"pubproxy_url"`
	Proxifly         bool   `mapstructure:"proxifly"`
	ProxiflyURL      string `mapstructure:"proxifly_url"`
	TheSpeedX        bool   `mapstructure:"thespeedx"`
	TheSpeedXURL     string `mapstructure:"thespeedx_url"`
	Monosans         bool   `mapstructure:"monosans"`
	MonosansURL      string `mapstructure:"monosans_url"`
	Gfpcom           bool   `mapstructure:"gfpcom"`
	GfpcomURL        string `mapstructure:"gfpcom_url"`
	Thordata         bool   `mapstructure:"thordata"`
	ThordataURL      string `mapstructure:"thordata_url"`
	Hproxy           bool   `mapstructure:"hproxy"`
	HproxyURL        string `mapstructure:"hproxy_url"`
	Sage520          bool   `mapstructure:"sage520"`
	Sage520URL       string `mapstructure:"sage520_url"`
	ErcinDedeoglu    bool   `mapstructure:"ercindedeoglu"`
	ErcinDedeogluURL string `mapstructure:"ercindedeoglu_url"`
}

func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}

// validateHTTPSURL requires an explicit https URL with a host and no
// embedded credentials.
func validateHTTPSURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: %s: invalid URL %q: %w", key, raw, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("config: %s must use https, got %q", key, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("config: %s must include a host, got %q", key, raw)
	}
	if u.User != nil {
		return fmt.Errorf("config: %s must not contain credentials, got %q", key, raw)
	}
	return nil
}

func validatePositiveDuration(key string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("config: %s must be positive, got %s", key, d)
	}
	return nil
}

// validateProxySettings fails startup on invalid explicit settings. Nothing
// is clamped or silently defaulted: a bad operator value must surface here,
// not as surprising scanner behavior.
func validateProxySettings(cfg *Config) error {
	proxy := cfg.Proxy
	if proxy == nil {
		return fmt.Errorf("config: [proxy] section missing")
	}
	if err := validateHTTPSURL("proxy.test_url", proxy.TestURL); err != nil {
		return err
	}
	if err := validatePositiveDuration("proxy.test_timeout", proxy.TestTimeout); err != nil {
		return err
	}
	if proxy.DeadThresholdDays <= 0 {
		return fmt.Errorf("config: proxy.dead_threshold_days must be positive, got %d", proxy.DeadThresholdDays)
	}
	if proxy.FailCountThreshold <= 0 {
		return fmt.Errorf("config: proxy.fail_count_threshold must be positive, got %d", proxy.FailCountThreshold)
	}

	scan := &proxy.Scan
	durations := []struct {
		key string
		d   time.Duration
	}{
		{"proxy.scan.verification_interval", scan.VerificationInterval},
		{"proxy.scan.freshness_ttl", scan.FreshnessTTL},
		{"proxy.scan.recovery_base", scan.RecoveryBase},
		{"proxy.scan.recovery_cap", scan.RecoveryCap},
		{"proxy.scan.recovery_horizon", scan.RecoveryHorizon},
		{"proxy.scan.lease_duration", scan.LeaseDuration},
		{"proxy.scan.min_repeat_interval", scan.MinRepeatInterval},
		{"proxy.scan.inconclusive_retry", scan.InconclusiveRetry},
		{"proxy.scan.control_interval", scan.ControlInterval},
	}
	for _, d := range durations {
		if err := validatePositiveDuration(d.key, d.d); err != nil {
			return err
		}
	}

	weights := [3]int{scan.WeightVerification, scan.WeightRecovery, scan.WeightBackground}
	weightKeys := [3]string{"proxy.scan.weight_verification", "proxy.scan.weight_recovery", "proxy.scan.weight_background"}
	for i, w := range weights {
		if w <= 0 {
			return fmt.Errorf("config: %s must be positive, got %d", weightKeys[i], w)
		}
	}
	if sum := weights[0] + weights[1] + weights[2]; sum != 100 {
		return fmt.Errorf("config: proxy.scan weights must sum to 100, got %d (verification=%d recovery=%d background=%d)",
			sum, weights[0], weights[1], weights[2])
	}

	// A freshness that expires before the bounded verification recheck can
	// never be honored, and a lease that may lapse before the check deadline
	// lets another replica claim a row that is still being scanned.
	if checkDeadline := scan.VerificationInterval + proxy.TestTimeout; scan.FreshnessTTL <= checkDeadline {
		return fmt.Errorf("config: proxy.scan.freshness_ttl (%s) must exceed verification_interval + test_timeout (%s)",
			scan.FreshnessTTL, checkDeadline)
	}
	if scan.LeaseDuration <= proxy.TestTimeout {
		return fmt.Errorf("config: proxy.scan.lease_duration (%s) must exceed test_timeout (%s)",
			scan.LeaseDuration, proxy.TestTimeout)
	}
	if scan.RecoveryCap < scan.RecoveryBase {
		return fmt.Errorf("config: proxy.scan.recovery_cap (%s) must be at least recovery_base (%s)",
			scan.RecoveryCap, scan.RecoveryBase)
	}
	if scan.MinRepeatInterval > scan.InconclusiveRetry {
		return fmt.Errorf("config: proxy.scan.min_repeat_interval (%s) must not exceed inconclusive_retry (%s)",
			scan.MinRepeatInterval, scan.InconclusiveRetry)
	}

	if err := validateHTTPSURL("proxy.consumer.test_url", proxy.Consumer.TestURL); err != nil {
		return err
	}
	if err := validatePositiveDuration("proxy.consumer.test_timeout", proxy.Consumer.TestTimeout); err != nil {
		return err
	}
	return validatePositiveDuration("proxy.consumer.cooldown", proxy.Consumer.Cooldown)
}

func NewConfig() (*Config, error) {
	loadDotEnv()

	content, err := configFS.ReadFile("config.toml")
	if err != nil {
		return nil, fmt.Errorf("read embedded config: %w", err)
	}

	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	v.SetConfigType("toml")
	if err := v.ReadConfig(bytes.NewBuffer(content)); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := validateProxySettings(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
