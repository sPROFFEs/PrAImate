package studio

import _ "embed"

// Assets are embedded into every launcher build and provisioned without Node
// tooling or access to the source checkout on the user's machine.

//go:embed extension/package.json
var ExtensionPackageJSON string

//go:embed extension/extension.js
var ExtensionJS string

//go:embed extension/rpc.js
var ExtensionRPCJS string

//go:embed extension/terminal.js
var ExtensionTerminalJS string

//go:embed extension/resources/chat.html
var ExtensionChatHTML string

//go:embed extension/resources/monke.svg
var ExtensionSVG string
