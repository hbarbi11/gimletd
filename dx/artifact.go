package dx

type Version struct {
	RepositoryName string   `json:"repositoryName,omitempty"`
	SHA            string   `json:"sha,omitempty"`
	Created        int64    `json:"created,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	Event          GitEvent `json:"event,omitempty"`
	SourceBranch   string   `json:"sourceBranch,omitempty"`
	TargetBranch   string   `json:"targetBranch,omitempty"`
	Tag            string   `json:"tag,omitempty"`
	AuthorName     string   `json:"authorName,omitempty"`
	AuthorEmail    string   `json:"authorEmail,omitempty"`
	CommitterName  string   `json:"committerName,omitempty"`
	CommitterEmail string   `json:"committerEmail,omitempty"`
	Message        string   `json:"message,omitempty"`
	URL            string   `json:"url,omitempty"`
}

// Artifact that contains all metadata that can be later used for releasing and auditing
type Artifact struct {
	ID string `json:"id,omitempty"`

	Created int64 `json:"created,omitempty"`

	// The releasable version
	Version Version `json:"version,omitempty"`

	// Arbitrary environment variables from CI
	Context map[string]string `json:"context,omitempty"`

	// The complete set of Gimlet environments from the Gimlet environment files
	Environments []*Manifest `json:"environments,omitempty"`

	// CI job information, test results, Docker image information, etc
	Items []map[string]interface{} `json:"items,omitempty"`
}

func (a *Artifact) HasCleanupPolicy() bool {
	for _, m := range a.Environments {
		if m.Cleanup != nil {
			return true
		}
	}
	return false
}

func (a *Artifact) Vars() map[string]string {
	vars := map[string]string{}

	for k, v := range a.Context {
		vars[k] = v
	}

	for _, values := range a.Items {
		for k, v := range values {
			if w, ok := v.(string); ok {
				vars[k] = w
			}
		}
	}
	return vars
}
