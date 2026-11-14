package config

import "testing"

func TestRequireHTTPSExceptLoopback(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{"https real host", "https://keycloak.example.com", false},
		{"http localhost", "http://localhost:8180", false},
		{"http 127.0.0.1", "http://127.0.0.1:8180", false},
		{"http ::1", "http://[::1]:8180", false},
		{"http real host rejected", "http://keycloak.example.com", true},
		{"http real host with path rejected", "http://internal-keycloak/realms/x", true},
		{"invalid URL rejected", "://not-a-url", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireHTTPSExceptLoopback(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("requireHTTPSExceptLoopback(%q) error = %v, wantErr %v", tt.rawURL, err, tt.wantErr)
			}
		})
	}
}

func TestLoadProviders(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{"none set", map[string]string{}, false},
		{"listmonk complete", map[string]string{"LISTMONK_HOST": "https://listmonk.example.com", "LISTMONK_API_USER": "u", "LISTMONK_API_TOKEN": "t"}, false},
		{"listmonk loopback http", map[string]string{"LISTMONK_HOST": "http://localhost:9000", "LISTMONK_API_USER": "u", "LISTMONK_API_TOKEN": "t"}, false},
		{"listmonk missing user", map[string]string{"LISTMONK_HOST": "https://listmonk.example.com", "LISTMONK_API_TOKEN": "t"}, true},
		{"listmonk missing token", map[string]string{"LISTMONK_HOST": "https://listmonk.example.com", "LISTMONK_API_USER": "u"}, true},
		{"listmonk plain http rejected", map[string]string{"LISTMONK_HOST": "http://listmonk.example.com", "LISTMONK_API_USER": "u", "LISTMONK_API_TOKEN": "t"}, true},
		{"listmonk credentials without host", map[string]string{"LISTMONK_API_USER": "u", "LISTMONK_API_TOKEN": "t"}, false},
		{"orca complete", map[string]string{"ORCA_API_URL": "https://orca.example.com", "ORCA_SYNC_TOKEN": "t"}, false},
		{"orca url only", map[string]string{"ORCA_API_URL": "https://orca.example.com"}, true},
		{"orca token only", map[string]string{"ORCA_SYNC_TOKEN": "t"}, true},
		{"orca plain http rejected", map[string]string{"ORCA_API_URL": "http://orca.example.com", "ORCA_SYNC_TOKEN": "t"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://x")
			t.Setenv("KEYCLOAK_HOST", "http://localhost:8180")
			t.Setenv("KEYCLOAK_REALM", "members")
			t.Setenv("KEYCLOAK_CLIENT_ID", "newsletter-system")
			for _, name := range []string{"LISTMONK_HOST", "LISTMONK_API_USER", "LISTMONK_API_TOKEN", "ORCA_API_URL", "ORCA_SYNC_TOKEN"} {
				t.Setenv(name, tt.env[name])
			}
			cfg, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (cfg.ListmonkHost != tt.env["LISTMONK_HOST"] || cfg.OrcaSyncToken != tt.env["ORCA_SYNC_TOKEN"]) {
				t.Errorf("Load() = %+v, env %v", cfg, tt.env)
			}
		})
	}
}
