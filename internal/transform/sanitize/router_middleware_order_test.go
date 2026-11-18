package sanitize

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Ensure multiple middlewares order is preserved when sanitizing router.
func TestRouterMiddlewareOrderPreserved(t *testing.T) {
	obj := map[string]interface{}{}
	require.NoError(t, json.Unmarshal([]byte(`{"entryPoints":["web"],"rule":"Path(`+"`/`"+`)","service":"svc","middlewares":["mw1","mw2","mw3"]}`), &obj))
	sanitizeRouter(obj, map[string]string{"svc": "svc"}, map[string]string{"mw1": "mw1", "mw2": "mw2", "mw3": "mw3"}, nil)
	require.Equal(t, []interface{}{"mw1", "mw2", "mw3"}, obj["middlewares"])
}
