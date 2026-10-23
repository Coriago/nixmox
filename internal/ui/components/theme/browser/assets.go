//go:build !js

package browser

import _ "embed"

//go:embed style.css
var CSS string
