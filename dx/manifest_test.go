package dx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_resolveVars(t *testing.T) {
	m := &Manifest{
		App:       "my-app",
		Namespace: "my-namespace",
		Values: map[string]interface{}{
			"image": "debian",
		},
	}

	err := m.ResolveVars(map[string]string{})
	assert.Nil(t, err)
	assert.Equal(t, "my-app", m.App)

	m = &Manifest{
		App:       "my-app-{{ .POSTFIX }}",
		Namespace: "my-namespace",
		Values: map[string]interface{}{
			"image": "debian:{{ .POSTFIX }}",
		},
	}

	err = m.ResolveVars(map[string]string{
		"POSTFIX": "test",
	})
	assert.Nil(t, err)
	assert.Equal(t, "my-app-test", m.App)
	assert.Equal(t, "debian:test", m.Values["image"])

	m = &Manifest{
		App:       "my-app-{{ .BRANCH | sanitizeDNSName }}",
		Namespace: "my-namespace",
		Values: map[string]interface{}{
			"image": "debian:{{ .BRANCH | sanitizeDNSName }}",
		},
	}

	err = m.ResolveVars(map[string]string{
		"BRANCH": "feature/my-feature",
	})
	assert.Nil(t, err)
	assert.Equal(t, "my-app-feature-my-feature", m.App)
	assert.Equal(t, "debian:feature-my-feature", m.Values["image"])
}

func Test_sanitizeDNSName(t *testing.T) {
	sanitized := sanitizeDNSName("CamelCase_with_snake")
	assert.Equal(t, "camelcase-with-snake", sanitized)

	sanitized = sanitizeDNSName("dependabot/npm_and_yarn/ws-5.2.3")
	assert.Equal(t, "dependabot-npm-and-yarn-ws-5-2-3", sanitized)

	sanitized = sanitizeDNSName("-can't start with dashes, nor end-")
	assert.Equal(t, "can-t-start-with-dashes-nor-end", sanitized)

	sanitized = sanitizeDNSName("!nope")
	assert.Equal(t, "nope", sanitized)

	sanitized = sanitizeDNSName("dope")
	assert.Equal(t, "dope", sanitized)
}
