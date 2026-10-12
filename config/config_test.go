package config

import (
	"os"
	"testing"
	"time"
)

func TestNewConfig_Defaults(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error loading defaults: %v", err)
	}

	if cfg.App.Name != "ffxiv-census" {
		t.Errorf("expected App.Name 'ffxiv-census', got %q", cfg.App.Name)
	}
	if cfg.App.BaseURL != "https://census.ffxivbard.com" {
		t.Errorf("expected App.BaseURL 'https://census.ffxivbard.com', got %q", cfg.App.BaseURL)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("expected HTTP.Port 8080, got %d", cfg.HTTP.Port)
	}
	if cfg.Postgres.User != "census" {
		t.Errorf("expected Postgres.User 'census', got %q", cfg.Postgres.User)
	}
	if cfg.Postgres.Database != "ffxiv_census" {
		t.Errorf("expected Postgres.Database 'ffxiv_census', got %q", cfg.Postgres.Database)
	}
	if cfg.Lodestone.RateLimit != 1.0 {
		t.Errorf("expected Lodestone.RateLimit 1.0, got %f", cfg.Lodestone.RateLimit)
	}
}

func TestNewConfig_EnvOverrides(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envVal   string
		validate func(t *testing.T, cfg *Config)
	}{
		{
			name:   "postgres dsn override",
			envKey: "POSTGRES_DSN",
			envVal: "postgres://custom:pass@localhost:5432/custom_db",
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Postgres.DSN != "postgres://custom:pass@localhost:5432/custom_db" {
					t.Errorf("expected Postgres.DSN 'postgres://custom:pass@localhost:5432/custom_db', got %q", cfg.Postgres.DSN)
				}
			},
		},
		{
			name:   "http port override",
			envKey: "HTTP_PORT",
			envVal: "9090",
			validate: func(t *testing.T, cfg *Config) {
				if cfg.HTTP.Port != 9090 {
					t.Errorf("expected HTTP.Port 9090, got %d", cfg.HTTP.Port)
				}
			},
		},
		{
			name:   "lodestone rate limit override",
			envKey: "LODESTONE_RATE_LIMIT",
			envVal: "5.5",
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Lodestone.RateLimit != 5.5 {
					t.Errorf("expected Lodestone.RateLimit 5.5, got %f", cfg.Lodestone.RateLimit)
				}
			},
		},
		{
			name:   "app env override",
			envKey: "APP_ENV",
			envVal: "production",
			validate: func(t *testing.T, cfg *Config) {
				if cfg.App.Env != "production" {
					t.Errorf("expected App.Env 'production', got %q", cfg.App.Env)
				}
			},
		},
		{
			name:   "app base url override",
			envKey: "APP_BASE_URL",
			envVal: "https://staging.ffxivbard.com",
			validate: func(t *testing.T, cfg *Config) {
				if cfg.App.BaseURL != "https://staging.ffxivbard.com" {
					t.Errorf("expected App.BaseURL 'https://staging.ffxivbard.com', got %q", cfg.App.BaseURL)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVal, exists := os.LookupEnv(tt.envKey)
			os.Setenv(tt.envKey, tt.envVal)
			defer func() {
				if exists {
					os.Setenv(tt.envKey, oldVal)
				} else {
					os.Unsetenv(tt.envKey)
				}
			}()

			cfg, err := NewConfig()
			if err != nil {
				t.Fatalf("unexpected error loading config: %v", err)
			}
			tt.validate(t, cfg)
		})
	}
}

func TestNewConfigRejectsExpiredBeforeRecheck(t *testing.T) {
	t.Setenv("PROXY_SCAN_FRESHNESS_TTL", "65s")
	t.Setenv("PROXY_SCAN_VERIFICATION_INTERVAL", "60s")
	t.Setenv("PROXY_TEST_TIMEOUT", "10s")
	if _, err := NewConfig(); err == nil {
		t.Fatal("accepted freshness that expires before the bounded recheck")
	}
}

func TestNewConfigRejectsLeaseWithinCheckDeadline(t *testing.T) {
	t.Setenv("PROXY_SCAN_LEASE_DURATION", "10s")
	t.Setenv("PROXY_TEST_TIMEOUT", "10s")
	if _, err := NewConfig(); err == nil {
		t.Fatal("accepted a scan lease that may expire before the check deadline")
	}
}

func TestNewConfigRejectsRecoveryCapBelowBase(t *testing.T) {
	t.Setenv("PROXY_SCAN_RECOVERY_CAP", "30s")
	if _, err := NewConfig(); err == nil {
		t.Fatal("accepted a recovery cap below the recovery base")
	}
}

func TestNewConfigRejectsMinRepeatAboveInconclusiveRetry(t *testing.T) {
	t.Setenv("PROXY_SCAN_MIN_REPEAT_INTERVAL", "2m")
	if _, err := NewConfig(); err == nil {
		t.Fatal("accepted a minimum repeat interval above the inconclusive retry")
	}
}

func TestNewConfigRejectsMalformedDuration(t *testing.T) {
	t.Setenv("PROXY_SCAN_FRESHNESS_TTL", "soon")
	if _, err := NewConfig(); err == nil {
		t.Fatal("accepted a malformed duration")
	}
}

func TestNewConfigRejectsNonPositiveScanDurations(t *testing.T) {
	keys := []string{
		"PROXY_TEST_TIMEOUT",
		"PROXY_SCAN_VERIFICATION_INTERVAL",
		"PROXY_SCAN_FRESHNESS_TTL",
		"PROXY_SCAN_RECOVERY_BASE",
		"PROXY_SCAN_RECOVERY_CAP",
		"PROXY_SCAN_RECOVERY_HORIZON",
		"PROXY_SCAN_LEASE_DURATION",
		"PROXY_SCAN_MIN_REPEAT_INTERVAL",
		"PROXY_SCAN_INCONCLUSIVE_RETRY",
		"PROXY_SCAN_CONTROL_INTERVAL",
		"PROXY_CONSUMER_TEST_TIMEOUT",
		"PROXY_CONSUMER_COOLDOWN",
	}
	for _, key := range keys {
		for _, val := range []string{"0s", "-1s"} {
			t.Run(key+"="+val, func(t *testing.T) {
				t.Setenv(key, val)
				if _, err := NewConfig(); err == nil {
					t.Fatalf("accepted %s=%q", key, val)
				}
			})
		}
	}
}

func TestNewConfigRejectsWeightsNotSummingTo100(t *testing.T) {
	for _, weights := range [][3]string{{"10", "50", "45"}, {"10", "35", "45"}} {
		t.Run(weights[0]+","+weights[1]+","+weights[2], func(t *testing.T) {
			t.Setenv("PROXY_SCAN_WEIGHT_VERIFICATION", weights[0])
			t.Setenv("PROXY_SCAN_WEIGHT_RECOVERY", weights[1])
			t.Setenv("PROXY_SCAN_WEIGHT_BACKGROUND", weights[2])
			if _, err := NewConfig(); err == nil {
				t.Fatalf("accepted weights %s/%s/%s that do not sum to 100", weights[0], weights[1], weights[2])
			}
		})
	}
}

func TestNewConfigRejectsNonPositiveWeight(t *testing.T) {
	for _, weights := range [][3]string{{"0", "45", "45"}, {"10", "-5", "45"}, {"10", "45", "0"}} {
		t.Run(weights[0]+","+weights[1]+","+weights[2], func(t *testing.T) {
			t.Setenv("PROXY_SCAN_WEIGHT_VERIFICATION", weights[0])
			t.Setenv("PROXY_SCAN_WEIGHT_RECOVERY", weights[1])
			t.Setenv("PROXY_SCAN_WEIGHT_BACKGROUND", weights[2])
			if _, err := NewConfig(); err == nil {
				t.Fatalf("accepted non-positive weight set %s/%s/%s", weights[0], weights[1], weights[2])
			}
		})
	}
}

func TestNewConfigRejectsInvalidTestURLs(t *testing.T) {
	cases := []struct{ key, val string }{
		{"PROXY_TEST_URL", "http://api64.ipify.org/?format=json"},
		{"PROXY_TEST_URL", "https://user:pass@api64.ipify.org/"},
		{"PROXY_TEST_URL", "https://"},
		{"PROXY_TEST_URL", "api64.ipify.org"},
		{"PROXY_CONSUMER_TEST_URL", "http://na.finalfantasyxiv.com/lodestone/"},
		{"PROXY_CONSUMER_TEST_URL", "https://user:pass@na.finalfantasyxiv.com/"},
		{"PROXY_CONSUMER_TEST_URL", "https://"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := NewConfig(); err == nil {
				t.Fatalf("accepted %s=%q", tc.key, tc.val)
			}
		})
	}
}

func TestNewConfigRejectsNonPositiveThresholds(t *testing.T) {
	t.Run("dead_threshold_days=0", func(t *testing.T) {
		t.Setenv("PROXY_DEAD_THRESHOLD_DAYS", "0")
		if _, err := NewConfig(); err == nil {
			t.Fatal("accepted dead_threshold_days=0")
		}
	})
	t.Run("fail_count_threshold=-1", func(t *testing.T) {
		t.Setenv("PROXY_FAIL_COUNT_THRESHOLD", "-1")
		if _, err := NewConfig(); err == nil {
			t.Fatal("accepted fail_count_threshold=-1")
		}
	})
}

func TestNewConfig_ProxyScanDefaults(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error loading defaults: %v", err)
	}
	scan := cfg.Proxy.Scan
	if scan.VerificationInterval != 60*time.Second {
		t.Errorf("expected verification_interval 60s, got %s", scan.VerificationInterval)
	}
	if scan.FreshnessTTL != 120*time.Second {
		t.Errorf("expected freshness_ttl 120s, got %s", scan.FreshnessTTL)
	}
	if scan.RecoveryBase != time.Minute {
		t.Errorf("expected recovery_base 1m, got %s", scan.RecoveryBase)
	}
	if scan.RecoveryCap != 15*time.Minute {
		t.Errorf("expected recovery_cap 15m, got %s", scan.RecoveryCap)
	}
	if scan.RecoveryHorizon != time.Hour {
		t.Errorf("expected recovery_horizon 1h, got %s", scan.RecoveryHorizon)
	}
	if scan.LeaseDuration != 30*time.Second {
		t.Errorf("expected lease_duration 30s, got %s", scan.LeaseDuration)
	}
	if scan.MinRepeatInterval != time.Second {
		t.Errorf("expected min_repeat_interval 1s, got %s", scan.MinRepeatInterval)
	}
	if scan.InconclusiveRetry != 60*time.Second {
		t.Errorf("expected inconclusive_retry 60s, got %s", scan.InconclusiveRetry)
	}
	if scan.ControlInterval != 30*time.Second {
		t.Errorf("expected control_interval 30s, got %s", scan.ControlInterval)
	}
	if [3]int{scan.WeightVerification, scan.WeightRecovery, scan.WeightBackground} != [3]int{10, 45, 45} {
		t.Errorf("expected weights 10/45/45, got %d/%d/%d", scan.WeightVerification, scan.WeightRecovery, scan.WeightBackground)
	}
	if cfg.Proxy.TestTimeout != 10*time.Second {
		t.Errorf("expected test_timeout 10s, got %s", cfg.Proxy.TestTimeout)
	}
	if cfg.Proxy.TestURL != "https://na.finalfantasyxiv.com/lodestone/" {
		t.Errorf("expected general test_url to default to the Lodestone URL, got %q", cfg.Proxy.TestURL)
	}
	if cfg.Proxy.Consumer.TestURL != "https://na.finalfantasyxiv.com/lodestone/" {
		t.Errorf("expected consumer test_url to default to the Lodestone URL, got %q", cfg.Proxy.Consumer.TestURL)
	}
	if cfg.Proxy.Consumer.TestTimeout != 10*time.Second {
		t.Errorf("expected consumer test_timeout 10s, got %s", cfg.Proxy.Consumer.TestTimeout)
	}
	if cfg.Proxy.Consumer.Cooldown != 60*time.Second {
		t.Errorf("expected consumer cooldown 60s, got %s", cfg.Proxy.Consumer.Cooldown)
	}
	if cfg.Proxy.Consumer.LockRefreshInterval != 100*time.Second {
		t.Errorf("expected consumer lock_refresh_interval 100s, got %s", cfg.Proxy.Consumer.LockRefreshInterval)
	}
}

func TestNewConfig_ProxyScanEnvOverrides(t *testing.T) {
	t.Setenv("PROXY_TEST_URL", "https://health.example.com/?format=json")
	t.Setenv("PROXY_TEST_TIMEOUT", "12s")
	t.Setenv("PROXY_SCAN_VERIFICATION_INTERVAL", "90s")
	t.Setenv("PROXY_SCAN_FRESHNESS_TTL", "150s")
	t.Setenv("PROXY_SCAN_RECOVERY_BASE", "2m")
	t.Setenv("PROXY_SCAN_RECOVERY_CAP", "20m")
	t.Setenv("PROXY_SCAN_RECOVERY_HORIZON", "2h")
	t.Setenv("PROXY_SCAN_WEIGHT_VERIFICATION", "20")
	t.Setenv("PROXY_SCAN_WEIGHT_RECOVERY", "50")
	t.Setenv("PROXY_SCAN_WEIGHT_BACKGROUND", "30")
	t.Setenv("PROXY_SCAN_LEASE_DURATION", "45s")
	t.Setenv("PROXY_SCAN_MIN_REPEAT_INTERVAL", "2s")
	t.Setenv("PROXY_SCAN_INCONCLUSIVE_RETRY", "90s")
	t.Setenv("PROXY_SCAN_CONTROL_INTERVAL", "45s")
	t.Setenv("PROXY_CONSUMER_TEST_URL", "https://destination.example.com/lodestone/")
	t.Setenv("PROXY_CONSUMER_TEST_TIMEOUT", "15s")
	t.Setenv("PROXY_CONSUMER_COOLDOWN", "90s")
	t.Setenv("PROXY_CONSUMER_LOCK_REFRESH_INTERVAL", "45s")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error loading overrides: %v", err)
	}
	if cfg.Proxy.TestURL != "https://health.example.com/?format=json" {
		t.Errorf("PROXY_TEST_URL override not applied: %q", cfg.Proxy.TestURL)
	}
	scan := cfg.Proxy.Scan
	if scan.VerificationInterval != 90*time.Second {
		t.Errorf("PROXY_SCAN_VERIFICATION_INTERVAL override not applied: %s", scan.VerificationInterval)
	}
	if scan.FreshnessTTL != 150*time.Second {
		t.Errorf("PROXY_SCAN_FRESHNESS_TTL override not applied: %s", scan.FreshnessTTL)
	}
	if scan.RecoveryBase != 2*time.Minute {
		t.Errorf("PROXY_SCAN_RECOVERY_BASE override not applied: %s", scan.RecoveryBase)
	}
	if scan.RecoveryCap != 20*time.Minute {
		t.Errorf("PROXY_SCAN_RECOVERY_CAP override not applied: %s", scan.RecoveryCap)
	}
	if scan.RecoveryHorizon != 2*time.Hour {
		t.Errorf("PROXY_SCAN_RECOVERY_HORIZON override not applied: %s", scan.RecoveryHorizon)
	}
	if scan.WeightVerification != 20 || scan.WeightRecovery != 50 || scan.WeightBackground != 30 {
		t.Errorf("weight overrides not applied: %d/%d/%d", scan.WeightVerification, scan.WeightRecovery, scan.WeightBackground)
	}
	if scan.LeaseDuration != 45*time.Second {
		t.Errorf("PROXY_SCAN_LEASE_DURATION override not applied: %s", scan.LeaseDuration)
	}
	if scan.MinRepeatInterval != 2*time.Second {
		t.Errorf("PROXY_SCAN_MIN_REPEAT_INTERVAL override not applied: %s", scan.MinRepeatInterval)
	}
	if scan.InconclusiveRetry != 90*time.Second {
		t.Errorf("PROXY_SCAN_INCONCLUSIVE_RETRY override not applied: %s", scan.InconclusiveRetry)
	}
	if scan.ControlInterval != 45*time.Second {
		t.Errorf("PROXY_SCAN_CONTROL_INTERVAL override not applied: %s", scan.ControlInterval)
	}
	if cfg.Proxy.TestTimeout != 12*time.Second {
		t.Errorf("PROXY_TEST_TIMEOUT override not applied: %s", cfg.Proxy.TestTimeout)
	}
	if cfg.Proxy.Consumer.TestURL != "https://destination.example.com/lodestone/" {
		t.Errorf("PROXY_CONSUMER_TEST_URL override not applied: %q", cfg.Proxy.Consumer.TestURL)
	}
	if cfg.Proxy.Consumer.TestTimeout != 15*time.Second {
		t.Errorf("PROXY_CONSUMER_TEST_TIMEOUT override not applied: %s", cfg.Proxy.Consumer.TestTimeout)
	}
	if cfg.Proxy.Consumer.Cooldown != 90*time.Second {
		t.Errorf("PROXY_CONSUMER_COOLDOWN override not applied: %s", cfg.Proxy.Consumer.Cooldown)
	}
	if cfg.Proxy.Consumer.LockRefreshInterval != 45*time.Second {
		t.Errorf("PROXY_CONSUMER_LOCK_REFRESH_INTERVAL override not applied: %s", cfg.Proxy.Consumer.LockRefreshInterval)
	}
}

// The lock must survive between refreshes with margin, so the refresh
// interval has to stay below half the lock TTL.
func TestNewConfigRejectsLockRefreshIntervalThatCouldLetTheLockExpire(t *testing.T) {
	cases := []struct{ name, ttl, interval string }{
		{"zero", "5m", "0s"},
		{"negative", "5m", "-1s"},
		{"half of ttl", "5m", "150s"},
		{"above half of ttl", "5m", "4m"},
		{"default interval with short ttl", "3m", "100s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROXY_CONSUMER_LOCK_TTL", tc.ttl)
			t.Setenv("PROXY_CONSUMER_LOCK_REFRESH_INTERVAL", tc.interval)
			if _, err := NewConfig(); err == nil {
				t.Fatalf("accepted lock_ttl=%s lock_refresh_interval=%s", tc.ttl, tc.interval)
			}
		})
	}
}
