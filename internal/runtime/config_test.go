package runtime

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestViperPrecedenceAndCatalogs(t *testing.T) {
	t.Setenv("NIXMOX_HTTP_PORT", "1") // Daemon settings do not use automatic env overrides.
	flags := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	AddFlags(flags)
	if err := flags.Parse([]string{"--data-dir=/tmp/explicit", "--discovery=false"}); err != nil {
		t.Fatal(err)
	}
	input := `http: {port: 64100}
data_dir: /tmp/file
discovery: {enable: true}
repositories:
  Lab.One: {remote_url: "https://example/config", credential_ref: GitKey}
credentials:
  GitKey: {kind: git-http-token, secret_file: /run/keys/git}
media:
  profiles:
    Pi.5: {label: Pi, system: aarch64-linux}
`
	config, err := Load(strings.NewReader(input), flags)
	if err != nil {
		t.Fatal(err)
	}
	if config.DataDir != "/tmp/explicit" || config.HTTP.Port != 64100 || config.Discovery.Enable || config.NATS != Defaults().NATS {
		t.Fatalf("incorrect precedence: %+v", config)
	}
	if config.Repositories["Lab.One"].UpstreamBranch != "main" || config.Credentials["GitKey"].SecretFile == "" || config.Media.Profiles["Pi.5"].Label != "Pi" {
		t.Fatalf("catalog aliases or defaults lost: %+v", config)
	}
	data, err := config.JSON()
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := Parse(data)
	if err != nil || roundtrip.Repositories["Lab.One"].RemoteURL == "" {
		t.Fatalf("module JSON roundtrip: %+v %v", roundtrip, err)
	}
}

func TestViperRejectsInvalidConfiguration(t *testing.T) {
	for _, input := range []string{
		"http: {unknown: true}", "http: {port: '64100'}", "http: {port: 64100.5}",
		"http: {port: 63171}", "data_dir: relative", "unknown: true", "null", "[]", "",
		"http: {port: 64100}\n---\nhttp: {port: 64101}", "http: {port: 64100, port: 64101}",
		"repositories: {Lab: {remote_url: 'https://example', unexpected: 1}}",
		"capabilities: {power: yes}", "network: {management_interfaces: eth0}",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse([]byte(input)); err == nil {
				t.Fatalf("accepted invalid configuration %q", input)
			}
		})
	}
}

func TestEndpointPrecedence(t *testing.T) {
	config := Defaults()
	config.HTTP.Port = 64100
	if Endpoint("http://explicit", "http://env", &config) != "http://explicit" || Endpoint("", "http://env", &config) != "http://env" || Endpoint("", "", &config) != "http://127.0.0.1:64100" {
		t.Fatal("endpoint precedence changed")
	}
}
