package provimux

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"path/filepath"
	"testing"
)

func TestReview21HomeGolden(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.AuthDir = dir
	cfg.Home.Enabled = true
	on := true
	cfg.Models.AnsweredBy = &on
	answeredBySwitch(t, true)
	_, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(dir, "config.yaml")).Build()
	if err != nil || logging.ProvimuxAnsweredBy() {
		t.Fatal("Home mode did not disable on startup", err)
	}
	m, _ := registerAnsweredByLists(t)
	checkAnsweredGolden(t, "models.json", answeredByModels(t, m, ""))
	checkAnsweredGolden(t, "models-accounts.json", answeredByModels(t, m, "?include=accounts"))
	checkAnsweredGolden(t, "model-routes.json", answeredByRoutes(t, m))
	checkAnsweredGolden(t, "answers.txt", answeredByAnswers(t, false))
}
