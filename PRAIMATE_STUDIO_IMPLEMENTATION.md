# PrAImate Studio — Diseño e implementación propuesta

## 1. Objetivo

**PrAImate Studio** será un entorno de desarrollo integrado basado en **Code-OSS**, distribuido y gestionado por PrAImate.

El objetivo no es crear una extensión convencional para el VS Code que ya tenga instalado el usuario, ni intentar incrustar un editor completo dentro de la ventana actual de PrAImate.

La propuesta es:

- añadir una nueva sección **Studio** en PrAImate;
- permitir instalar, actualizar, reparar y abrir una build propia basada en Code-OSS;
- integrar PrAImate de forma nativa dentro de esa build;
- reutilizar toda la lógica existente de agentes, workflows, chats, MCP, skills, modelos, CLIs, permisos y ejecución;
- mantener una única fuente de verdad: **PrAImate Core**;
- usar Studio como frontend especializado para programación.

La experiencia objetivo es:

```text
PrAImate
   │
   ├── Code
   ├── Chats
   ├── Agents
   ├── Workflows
   ├── Skills
   ├── MCP
   ├── ...
   │
   └── Studio
          │
          ├── Install
          ├── Update
          ├── Repair
          └── Open project
                    │
                    ▼
             PrAImate Studio
             (managed Code-OSS)
```

---

# 2. Principio arquitectónico

PrAImate Studio debe ser **un frontend de PrAImate**, no una segunda implementación de PrAImate.

La arquitectura debería quedar así:

```text
                         ┌─────────────────────┐
                         │    PrAImate Core    │
                         │                     │
                         │ Agents              │
                         │ Workflows           │
                         │ Chats / Sessions    │
                         │ Skills              │
                         │ MCP                 │
                         │ Models              │
                         │ CLI integrations    │
                         │ PrAImate Code       │
                         │ Permissions         │
                         │ Secrets / Storage   │
                         │ Runtime             │
                         └─────────┬───────────┘
                                   │
                          Application API / RPC
                                   │
               ┌───────────────────┴───────────────────┐
               │                                       │
       PrAImate Desktop                         PrAImate Studio
                                                       │
                                              Code-OSS Workbench
                                                       │
                                              Built-in integration
```

La regla más importante:

> **Studio nunca debe acceder directamente a la base de datos, secretos o configuración interna de PrAImate.**

Toda operación debe pasar por una API estable de PrAImate.

---

# 3. Qué es PrAImate Studio

PrAImate Studio será una distribución de Code-OSS mantenida por el proyecto PrAImate.

Conceptualmente:

```text
Code-OSS upstream
       │
       ├── branding PrAImate
       ├── product configuration
       ├── Open VSX
       ├── built-in PrAImate integration
       ├── launcher integration
       └── minimal patch set
               │
               ▼
        PrAImate Studio
```

No debería ser un fork profundo.

La estrategia de mantenimiento debe ser:

```text
95-99% Code-OSS upstream
+
branding
+
build configuration
+
built-in extension
+
mínimos patches inevitables
```

Cuanto menos código de `src/vs/...` se modifique, mejor.

---

# 4. Qué NO hacer

## 4.1. No incrustar Electron dentro de PrAImate

No intentar:

```text
PrAImate window
┌──────────────────────────────────┐
│ Chats | Agents | Studio          │
├──────────────────────────────────┤
│                                  │
│    Code-OSS / Electron embebido  │
│                                  │
└──────────────────────────────────┘
```

Esto introduce problemas de:

- lifecycle;
- ventanas nativas;
- X11/Wayland;
- HWND en Windows;
- foco;
- teclado;
- clipboard;
- drag & drop;
- rendering;
- DPI;
- ventanas secundarias;
- Electron dentro de otra shell;
- mantenimiento multiplataforma.

La pestaña **Studio** en PrAImate debe ser un launcher/control center.

Studio se abre como una aplicación separada:

```text
PrAImate
   │
   └── Studio
         │
         └── Open
               │
               ▼
      PrAImate Studio window
```

---

## 4.2. No duplicar lógica de negocio

Evitar:

```text
PrAImate Desktop → implementación A
PrAImate Studio  → implementación B
```

Ejemplos de cosas que no debe reimplementar Studio:

- lectura/escritura directa de agentes;
- almacenamiento de chats;
- gestión de secrets;
- instalación de MCP;
- routing de modelos;
- instalación de CLIs;
- permisos;
- ejecución de workflows;
- lógica de approvals;
- persistencia de sesiones.

Studio solo presenta UI y contexto de editor.

---

## 4.3. No convertir la primera versión en un competidor de Cursor

No empezar por:

- inline completion;
- ghost text;
- predicción continua;
- custom debugger;
- reescribir IntelliSense;
- modificar profundamente el Workbench;
- reemplazar LSP;
- crear una shell propia de editor.

La ventaja inicial de PrAImate es el flujo completo de ejecución y orquestación.

---

# 5. Experiencia de usuario

## 5.1. Nueva pestaña `Studio` en PrAImate

Ejemplo:

```text
STUDIO

PrAImate Studio
────────────────────────────────────────

Status
✓ Installed

Version
0.1.0

Code-OSS base
1.xx.x

[ Open Studio ]
[ Update ]
[ Repair ]

────────────────────────────────────────

OPEN PROJECT

Workspace
~/projects/backend-api

CLI
PrAImate Code

Model
Default

Agent
Backend Developer

Permissions
Edits

MCP
filesystem, github

Skills
testing, security

[ Open in Studio ]

────────────────────────────────────────

RECENT

backend-api
praimate
coresecframe
my-project
```

La configuración inicial puede servir para crear una sesión de Studio, pero la configuración canónica sigue perteneciendo a PrAImate.

---

# 6. Lanzamiento

Debe poder abrirse desde la GUI:

```text
PrAImate → Studio → Open
```

y desde CLI:

```bash
praimate studio .
```

o:

```bash
praimate studio /path/to/project
```

También:

```bash
praimate studio install
praimate studio update
praimate studio repair
praimate studio status
```

---

# 7. Runtime

Studio no debería depender de que la GUI principal esté abierta.

Flujo:

```text
praimate studio .
       │
       ▼
¿PrAImate backend disponible?
       │
   ┌───┴───┐
   │       │
  sí      no
   │       │
   │       └── start managed backend
   │
   ▼
create Studio session
   │
   ▼
launch praimate-studio
   │
   ▼
connect built-in integration
```

Esto permite que PrAImate Studio sea utilizable como entorno de desarrollo habitual.

---

# 8. PrAImate Backend Service

Se necesita una forma estable de acceder a PrAImate desde Studio.

Propuesta:

```bash
praimate internal serve
```

o:

```bash
praimate serve --mode=studio
```

Debe proporcionar:

- transporte local;
- autenticación local;
- lifecycle;
- API versionada;
- eventos en tiempo real;
- cancelación;
- approvals;
- sesiones persistentes.

---

# 9. Transporte

Para v1 elegiría uno de estos mecanismos:

## Opción A — Unix socket / Named Pipe

Preferida para desktop local.

Linux/macOS:

```text
~/.config/praimate/run/praimate.sock
```

Windows:

```text
\\.\pipe\praimate
```

Ventajas:

- local;
- rápido;
- no expone un puerto;
- fácil de restringir al usuario;
- apropiado para IPC.

---

## Opción B — stdio

Útil para procesos hijos y herramientas simples:

```text
praimate internal serve --stdio
```

Puede utilizarse inicialmente para prototipado.

---

## Opción C — localhost HTTP/WebSocket

Puede ser útil más adelante, pero no sería la primera elección.

Si se implementa:

```text
127.0.0.1:<random-port>
```

con token de sesión obligatorio.

No escuchar en interfaces externas por defecto.

---

# 10. Protocolo

Usaría JSON-RPC 2.0 o un protocolo equivalente, versionado explícitamente.

Ejemplo:

```text
praimate.rpc/v1
```

Handshake:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "system.initialize",
  "params": {
    "client": "praimate-studio",
    "clientVersion": "0.1.0",
    "protocolVersion": "1"
  }
}
```

Respuesta:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "serverVersion": "0.9.0",
    "protocolVersion": "1",
    "capabilities": {
      "agents": true,
      "workflows": true,
      "skills": true,
      "mcp": true,
      "localModels": true,
      "approvals": true,
      "streaming": true
    }
  }
}
```

---

# 11. API propuesta

## 11.1. Sistema

```text
system.initialize
system.status
system.version
system.capabilities
system.shutdown
system.unlock
```

---

## 11.2. Projects / Workspaces

```text
projects.list
projects.get
projects.open
projects.add
projects.remove
projects.recent
```

Studio puede aportar contexto adicional:

```text
workspace.register
workspace.getContext
workspace.changed
```

---

## 11.3. Chats

```text
chats.list
chats.get
chats.create
chats.delete
chats.rename
chats.send
chats.stop
chats.resume
```

---

## 11.4. Agents

```text
agents.list
agents.get
agents.create
agents.update
agents.delete
agents.import
agents.export
agents.run
```

---

## 11.5. Workflows

```text
workflows.list
workflows.get
workflows.create
workflows.update
workflows.delete
workflows.run
```

---

## 11.6. Runs

```text
runs.list
runs.get
runs.cancel
runs.resume
runs.approve
runs.deny
runs.subscribe
```

---

## 11.7. Skills

```text
skills.list
skills.get
skills.install
skills.enable
skills.disable
skills.remove
skills.update
```

---

## 11.8. MCP

```text
mcp.list
mcp.get
mcp.create
mcp.update
mcp.remove
mcp.enable
mcp.disable
mcp.test
```

---

## 11.9. Models

```text
models.list
models.get
models.routes
models.setRoute
models.test
```

---

## 11.10. Tools / CLIs

```text
tools.list
tools.detect
tools.install
tools.update
tools.repair
tools.remove
```

---

## 11.11. Settings

```text
settings.get
settings.update
settings.reset
```

Los settings de Studio que sean puramente visuales pueden quedarse en Code-OSS.

Ejemplo:

```json
{
  "praimateStudio.panelLocation": "sidebar",
  "praimateStudio.autoConnect": true
}
```

Pero la configuración de PrAImate debe vivir en PrAImate.

---

# 12. Eventos

El backend debe emitir eventos.

Ejemplo:

```text
run.started
run.updated
run.output
run.tool.started
run.tool.completed
run.tool.failed

run.approval.required

run.file.read
run.file.modified
run.command.started
run.command.completed

run.completed
run.failed
run.cancelled

chat.created
chat.updated

agent.changed
workflow.changed
skill.changed
mcp.changed
settings.changed
```

Ejemplo:

```json
{
  "jsonrpc": "2.0",
  "method": "run.approval.required",
  "params": {
    "runId": "run_123",
    "approvalId": "approval_456",
    "type": "command",
    "command": "npm test",
    "cwd": "/workspace/project"
  }
}
```

---

# 13. Built-in PrAImate integration

PrAImate Studio debe incluir una extensión built-in que el usuario no tenga que instalar.

Ejemplo de estructura:

```text
praimate-studio/
├── upstream/
├── patches/
├── product/
├── extensions/
│   └── praimate/
│       ├── package.json
│       ├── src/
│       │   ├── extension.ts
│       │   ├── rpc/
│       │   ├── views/
│       │   ├── editor/
│       │   ├── git/
│       │   ├── terminal/
│       │   └── approvals/
│       └── webview/
└── build/
```

La extensión built-in será la mayor parte de la integración.

Los cambios directos a Code-OSS deben minimizarse.

---

# 14. Activity Bar

Añadir una sección:

```text
Explorer
Search
Source Control
Run
Extensions

PrAImate
```

Dentro:

```text
PRAIMATE

SESSION
  Current project
  Active model
  Active CLI
  Permissions

CHATS
  + New
  Implement authentication
  Review API
  Fix tests

AGENTS
  Backend Developer
  Security Reviewer
  Pentester

WORKFLOWS
  Security Audit
  Implement Feature
  Code Review

SKILLS
  testing
  security

MCP
  filesystem ✓
  github ✓

RUNS
  Current task
  Previous tasks
```

---

# 15. Chat / Task UI

Para chats y ejecuciones sí tiene sentido utilizar Webviews.

Ejemplo:

```text
main.go | auth.go | PrAImate: Implement JWT

┌────────────────────────────────────────────┐
│ Implement JWT                             │
│ Backend Developer · PrAImate Code         │
├────────────────────────────────────────────┤
│                                            │
│ You                                        │
│ Implement refresh token support.           │
│                                            │
│ PrAImate                                   │
│ Inspecting authentication module...        │
│                                            │
│ ✓ Read src/auth/token.ts                   │
│ ✓ Read src/auth/session.ts                 │
│ ✓ Plan created                             │
│ ● Editing token.ts                         │
│ ○ Run tests                                │
│ ○ Review changes                           │
│                                            │
├────────────────────────────────────────────┤
│ Ask PrAImate...                      Send  │
└────────────────────────────────────────────┘
```

---

# 16. Contexto del editor

Studio puede proporcionar a PrAImate contexto que la GUI normal no conoce.

Ejemplo:

```json
{
  "workspace": "/project",
  "activeFile": "src/auth/token.ts",
  "language": "typescript",
  "selection": {
    "startLine": 42,
    "endLine": 68,
    "text": "..."
  },
  "visibleFiles": [
    "src/auth/token.ts",
    "src/auth/session.ts"
  ],
  "dirtyFiles": [
    "src/auth/token.ts"
  ]
}
```

Debe evitarse enviar contexto continuamente sin necesidad.

El backend debe solicitarlo según la tarea.

---

# 17. Operaciones sobre código

La integración debe aprovechar APIs nativas del editor.

Ejemplos:

```text
PrAImate: Ask about selection
PrAImate: Explain
PrAImate: Refactor
PrAImate: Fix
PrAImate: Generate tests
PrAImate: Review file
PrAImate: Review Git changes
PrAImate: Implement TODO
PrAImate: Run agent
PrAImate: Run workflow
```

Estas son entradas rápidas al mismo runtime.

No son implementaciones independientes.

---

# 18. Aplicación de cambios

Este punto es importante.

PrAImate no debería escribir directamente archivos que están abiertos y modificados en Studio sin coordinarse con el editor.

Flujo recomendado:

```text
PrAImate generates patch
       │
       ▼
Studio receives proposed edit
       │
       ▼
native diff
       │
       ├── Accept
       └── Reject
```

Para cambios automáticos autorizados:

```text
backend
   │
   ▼
Studio integration
   │
   ▼
WorkspaceEdit
```

Así se respetan:

- buffers sin guardar;
- undo;
- dirty state;
- editor history;
- múltiples archivos;
- experiencia nativa.

---

# 19. Approvals

Los approvals deben ser first-class.

## Comando

```text
PrAImate requests permission

Run command:

npm test

Working directory:
/project

[ Allow once ]
[ Allow for this run ]
[ Deny ]
```

## Escritura

```text
PrAImate wants to modify:

src/auth/token.ts
src/auth/session.ts
tests/auth.test.ts

[ Review Changes ]
[ Allow ]
[ Deny ]
```

## Red

```text
PrAImate requests network access

https://api.example.com

[ Allow once ]
[ Allow for this run ]
[ Deny ]
```

---

# 20. Git

Integración inicial útil:

```text
Source Control
     │
     └── PrAImate: Review changes
```

PrAImate recibe:

- diff staged;
- diff unstaged;
- branch;
- changed files;
- opcionalmente contexto relacionado.

Resultado:

```text
PrAImate Review

src/auth.ts:82
Potential authorization issue

[ Go to issue ]
[ Explain ]
[ Fix ]
```

---

# 21. Terminal

No crear una terminal HTML propia.

Usar terminales nativas de Code-OSS.

Ejemplo:

```text
TERMINAL

PrAImate Code
```

PrAImate puede lanzar:

- PrAImate Code;
- Claude Code;
- Codex;
- OpenCode;
- shell commands;
- tests;
- builds.

La terminal visual sigue perteneciendo al editor.

---

# 22. PrAImate Code

PrAImate Code debe ser la opción predeterminada o recomendada dentro de Studio cuando proceda.

Arquitectura:

```text
               PrAImate Core
                    │
          ┌─────────┴─────────┐
          │                   │
  PrAImate Studio       PrAImate Code
          │                   │
          └─────────┬─────────┘
                    │
              shared runtime
```

No deben convertirse en productos aislados.

---

# 23. Agents

Los agentes deben aparecer dentro de Studio.

Ejemplo:

```text
AGENTS

Backend Developer
Security Reviewer
Pentester
Researcher

+ New Agent
```

Inicialmente puede abrirse un editor visual/webview equivalente al Agent Studio actual.

Toda modificación llama al backend:

```text
agents.get
agents.update
```

Nunca editar archivos internos directamente desde la extensión.

---

# 24. Workflows

Mismo patrón:

```text
WORKFLOWS

Security Audit
Implement Feature
Review Pull Request
Generate Tests
```

Ejecutar:

```text
workflow.run
```

El resultado aparece como Run/Task y puede interactuar con el workspace.

---

# 25. Skills

UI simple:

```text
SKILLS

✓ testing
✓ security-review
✓ refactor
○ docs

[ Add ]
```

El backend sigue siendo responsable de:

- instalación;
- actualización;
- validación;
- permisos;
- almacenamiento.

---

# 26. MCP

Ejemplo:

```text
MCP SERVERS

● filesystem
● github
○ postgres
● browser

+ Add Server
```

Los secrets nunca deben guardarse en:

```text
.vscode/settings.json
```

Deben permanecer en PrAImate.

Studio solo envía formularios al backend.

---

# 27. Modelos locales y remotos

Vista:

```text
MODELS

Default route
Claude Sonnet

Local
Ollama
http://127.0.0.1:11434/v1

Model
qwen3-coder

● Connected
```

Operaciones:

```text
models.routes
models.setRoute
models.test
```

---

# 28. CLI & Tools

Dentro de Studio:

```text
TOOLS

PrAImate Code
✓ Installed

Claude Code
✓ Installed

Codex
Update available

OpenCode
✓ Installed

[ Detect ]
[ Update ]
[ Repair ]
```

El backend ejecuta las operaciones.

Studio solo las presenta.

---

# 29. Settings

Separar:

## Settings de PrAImate

Fuente canónica:

```text
PrAImate Core
```

Ejemplos:

- modelos;
- MCP;
- agents;
- permissions;
- skills;
- paths;
- CLI config;
- secrets;
- runtime.

## Settings de Studio

Pueden vivir en configuración de Code-OSS:

```text
praimateStudio.autoConnect
praimateStudio.showStatusBar
praimateStudio.defaultView
praimateStudio.openChatBeside
```

---

# 30. Distribución

PrAImate gestionará Studio igual que otras herramientas.

Ejemplo:

```text
~/.config/praimate/
├── tools/
│   ├── praimate-code/
│   │   └── <version>/
│   │
│   └── praimate-studio/
│       ├── <version>/
│       └── current
│
├── agents/
├── skills/
├── runtime/
└── ...
```

La ruta real debe adaptarse a cada sistema operativo.

---

# 31. Studio manifest

Cada versión debería incluir metadata:

```json
{
  "name": "praimate-studio",
  "version": "0.1.0",
  "codeOssVersion": "1.xx.x",
  "protocolVersion": "1",
  "platform": "linux-x64",
  "buildId": "..."
}
```

PrAImate verifica compatibilidad antes de lanzarlo.

---

# 32. Instalación

Desde Desktop:

```text
Studio

Not installed

[ Install ]
```

Backend:

```text
download
→ verify checksum/signature
→ unpack
→ register version
→ mark current
```

Estados:

```text
Not installed
Installing
Installed
Update available
Broken
Repairing
Unsupported
```

---

# 33. Actualizaciones

Studio debe tener un ciclo independiente de PrAImate Core, pero gestionado desde PrAImate.

Ejemplo:

```text
PrAImate 0.9.x
supports Studio protocol v1

Studio 0.1.x
protocol v1
```

No acoplar:

```text
PrAImate 0.9.1 == Studio 0.9.1
```

Mejor mantener compatibilidad por protocolo.

---

# 34. Seguridad de actualizaciones

Necesario:

- checksums;
- firmas;
- HTTPS;
- releases identificables;
- rollback;
- atomic install;
- no reemplazar una versión en ejecución.

Flujo:

```text
download new version
       │
       ▼
verify
       │
       ▼
install side-by-side
       │
       ▼
switch "current"
       │
       ▼
keep previous version temporarily
```

---

# 35. Extension marketplace

No depender del Visual Studio Marketplace como requisito estructural.

Configurar Studio con:

```text
Open VSX
```

como registry principal.

La integración de PrAImate será built-in, así que no depende de ningún marketplace.

---

# 36. Repositorios

## Opción recomendada

Mantener Studio separado del repo principal de PrAImate:

```text
sPROFFEs/PrAImate
sPROFFEs/PrAImate-Code
sPROFFEs/PrAImate-Studio
```

o equivalente.

Razones:

- Code-OSS pesa mucho;
- lifecycle diferente;
- CI diferente;
- releases distintas;
- upstream sync independiente.

El repositorio principal solo necesita conocer:

```text
Studio release manifest
Studio installer/updater
Studio launcher
Studio protocol
```

---

# 37. Estructura de `PrAImate-Studio`

Ejemplo:

```text
PrAImate-Studio/
├── README.md
├── upstream/
│
├── patches/
│   ├── 0001-branding.patch
│   └── ...
│
├── product/
│   ├── product.json
│   ├── icons/
│   └── branding/
│
├── extensions/
│   └── praimate/
│       ├── package.json
│       ├── src/
│       ├── media/
│       └── webview/
│
├── scripts/
│   ├── sync-upstream.*
│   ├── apply-patches.*
│   ├── build.*
│   └── package.*
│
├── ci/
└── docs/
```

---

# 38. Upstream strategy

Mantener una referencia clara:

```text
CODE_OSS_VERSION=...
CODE_OSS_COMMIT=...
```

Proceso:

```text
1. update upstream ref
2. rebuild
3. apply minimal patches
4. run integration tests
5. fix breakage
6. publish Studio build
```

Evitar commits manuales mezclados por todo el árbol upstream.

---

# 39. Built-in extension vs patches

Prioridad:

```text
1. VS Code Extension API
2. product.json/configuration
3. built-in extension
4. contribution points
5. minimal upstream patch
```

Solo modificar Workbench cuando no exista alternativa razonable.

---

# 40. Primera versión viable

## V0.1 — Managed Studio

Objetivo: demostrar distribución y conexión.

Incluye:

- Code-OSS build;
- branding PrAImate;
- Open VSX;
- extensión PrAImate built-in;
- sección Studio en PrAImate;
- install/update/repair;
- `praimate studio`;
- conexión con backend;
- status bar;
- workspace actual;
- selector de modelo;
- selector de agente;
- selector de CLI;
- abrir PrAImate Code en terminal;
- chat básico.

No incluye todavía una reconstrucción completa del frontend de PrAImate.

---

# 41. V0.2 — Programming workflow

Añadir:

- selection context;
- active file;
- diagnostics;
- Git diff;
- dirty buffers;
- native diff;
- WorkspaceEdit;
- approvals;
- tasks/runs;
- cancellation;
- streaming;
- commands:

```text
Ask
Explain
Fix
Refactor
Generate tests
Review file
Review changes
Implement TODO
```

---

# 42. V0.3 — Full PrAImate workflow

Añadir:

- Chats completos;
- Agents CRUD;
- Workflows;
- Skills;
- MCP;
- Models;
- CLI & Tools;
- saved sessions;
- artifacts;
- configuration;
- import/export.

En este punto Studio puede reemplazar casi por completo la GUI desktop durante desarrollo.

---

# 43. V0.4 — Deep integration

Solo después de validar el producto:

- inline interaction;
- richer Code Actions;
- diagnostics generated by PrAImate;
- Test integration;
- source control integration;
- code lenses;
- task decorations;
- custom editors;
- richer project awareness.

---

# 44. Posible V1

La definición de V1 debería ser:

> Un desarrollador puede instalar PrAImate, activar Studio, abrir un proyecto y utilizar el flujo completo de PrAImate sin abandonar el entorno de desarrollo.

Debe poder:

1. abrir proyecto;
2. iniciar chat;
3. ejecutar PrAImate Code;
4. seleccionar modelo;
5. seleccionar agente;
6. ejecutar workflow;
7. usar Skills;
8. usar MCP;
9. recibir approvals;
10. revisar cambios;
11. aceptar/rechazar modificaciones;
12. lanzar tests/comandos;
13. consultar ejecuciones;
14. gestionar agentes;
15. reutilizar las mismas sesiones/configuración que Desktop.

---

# 45. Application API antes que UI

Antes de implementar todas las vistas de Studio, conviene refactorizar PrAImate para que cada acción de negocio sea accesible sin depender de la GUI.

Ejemplo conceptual:

```text
Application Layer

CreateChat()
SendMessage()

ListAgents()
GetAgent()
CreateAgent()
UpdateAgent()

ListWorkflows()
RunWorkflow()

InstallSkill()
EnableSkill()

CreateMCPServer()
TestMCPServer()

ListTools()
InstallTool()

ApproveRunAction()
CancelRun()
```

Después:

```text
Desktop UI
    │
    └── Application Layer

RPC Server
    │
    └── Application Layer
          ▲
          │
     Studio integration
```

Esta separación es probablemente el trabajo arquitectónico más importante.

---

# 46. Sesiones de Studio

Crear una entidad de sesión:

```json
{
  "id": "studio_123",
  "workspace": "/project",
  "createdAt": "...",
  "cli": "praimate-code",
  "model": "default",
  "agent": "backend-developer",
  "permissions": "edits"
}
```

No tiene por qué ser una sesión persistente siempre.

Puede servir para:

- lifecycle;
- routing;
- permisos;
- eventos;
- reconnect;
- multi-window.

---

# 47. Múltiples ventanas

El backend debe asumir:

```text
Studio Window A
/project-a

Studio Window B
/project-b

PrAImate Desktop
```

conectados simultáneamente.

Por tanto no usar estado global como:

```text
currentProject
currentAgent
currentRun
```

Debe existir estado por cliente/sesión/workspace.

---

# 48. Reconnect

Si el backend reinicia:

```text
Studio
   │
connection lost
   │
retry
   │
new handshake
   │
restore session
```

Los runs persistentes deben poder reengancharse mediante:

```text
runs.subscribe
```

---

# 49. Unlock

Si PrAImate está bloqueado:

```text
Studio starts
   │
   ▼
backend: locked
   │
   ▼
Studio prompt
   │
   ▼
system.unlock
```

La contraseña se entrega únicamente al backend.

No persistirla en la extensión.

---

# 50. Estado de conexión

Status bar:

```text
◉ PrAImate
```

Estados:

```text
◉ Connected
◌ Connecting
! Locked
! Update required
× Disconnected
```

Click:

```text
PrAImate Status
Open Dashboard
Reconnect
Unlock
Logs
```

---

# 51. Logs

Separar:

```text
PrAImate Core logs
Studio integration logs
Code-OSS logs
```

Desde Studio:

```text
Output
→ PrAImate
```

Nunca imprimir secrets.

---

# 52. Diagnóstico

Comando:

```text
PrAImate: Diagnostics
```

Debe mostrar:

```text
PrAImate Core: 0.x.x
Studio: 0.x.x
Protocol: v1
Workspace: ...
Backend: connected
PrAImate Code: installed
MCP servers: ...
```

Con una opción:

```text
Copy diagnostic report
```

filtrando datos sensibles.

---

# 53. Testing

## Unit

- protocolo;
- serialización;
- compatibility;
- session state;
- approvals.

## Integration

```text
Studio integration
      ↕
PrAImate test backend
```

Casos:

- connect;
- reconnect;
- chat;
- run;
- cancel;
- approve;
- reject;
- patch;
- WorkspaceEdit.

## E2E

Lanzar Studio y comprobar:

```text
open workspace
→ execute PrAImate command
→ backend receives context
→ generates edit
→ Studio applies edit
```

---

# 54. CI

Build matrix inicial:

```text
Linux x64
Windows x64
```

Después:

```text
Linux arm64
Windows arm64
macOS
```

No intentaría soportar todas las plataformas desde el primer commit si actualmente PrAImate tampoco las distribuye todas.

---

# 55. Release pipeline

```text
Code-OSS upstream
       │
       ▼
sync
       │
       ▼
apply patches
       │
       ▼
inject built-in PrAImate extension
       │
       ▼
build
       │
       ▼
test
       │
       ▼
package
       │
       ▼
sign / checksum
       │
       ▼
publish release manifest
       │
       ▼
PrAImate updater detects version
```

---

# 56. Version manifest remoto

Ejemplo:

```json
{
  "latest": "0.2.0",
  "protocol": 1,
  "releases": {
    "linux-x64": {
      "url": "...",
      "sha256": "..."
    },
    "windows-x64": {
      "url": "...",
      "sha256": "..."
    }
  }
}
```

---

# 57. Riesgos principales

## 57.1. Mantener Code-OSS

Es el mayor coste.

Mitigación:

- pin upstream;
- pocos patches;
- built-in extension;
- actualizaciones periódicas;
- CI automatizado.

---

## 57.2. Duplicar frontend

Implementar todo PrAImate dos veces puede consumir demasiado.

Mitigación:

Primero priorizar programación:

```text
Sessions
Chat
Agents
Runs
Approvals
Code operations
```

Y añadir configuración avanzada gradualmente.

Desktop puede seguir siendo el panel completo de administración durante las primeras versiones.

---

## 57.3. API interna inestable

Si Studio consume internals directamente, cada refactor romperá Studio.

Mitigación:

```text
praimate.rpc/v1
```

y contratos claros.

---

## 57.4. Estado duplicado

Mitigación:

PrAImate Core siempre es source of truth.

Studio mantiene únicamente:

- UI state;
- editor state;
- workspace state.

---

## 57.5. Marketplace/extensions

Mitigación:

- Open VSX default;
- no depender de extensiones propietarias;
- documentar incompatibilidades.

---

# 58. Decisiones que dejaría cerradas desde el inicio

1. El producto se llama **PrAImate Studio**.
2. Está basado en **Code-OSS**.
3. Se distribuye como herramienta gestionada por PrAImate.
4. Se abre como proceso/ventana independiente.
5. La pestaña Studio del Desktop actúa como launcher y manager.
6. La lógica de negocio permanece en PrAImate Core.
7. Studio usa una API/RPC versionada.
8. La integración de PrAImate viene built-in.
9. Open VSX es el registry por defecto.
10. Los cambios a Code-OSS deben ser mínimos.
11. PrAImate Code será un engine de coding de primera clase dentro de Studio.
12. Desktop y Studio comparten estado, sesiones, agentes, MCP, skills y configuración.
13. Los secretos nunca se almacenan en los settings de Studio.
14. Los cambios sobre buffers abiertos se coordinan mediante APIs del editor.
15. Studio debe poder funcionar aunque la GUI principal de PrAImate no esté abierta.

---

# 59. Orden de implementación recomendado

## Fase 1 — Core boundary

Crear una capa de aplicación estable dentro de PrAImate.

Objetivo:

```text
GUI-independent business operations
```

---

## Fase 2 — RPC

Implementar:

```text
system.*
projects.*
chats.*
runs.*
agents.*
```

más eventos y approvals.

---

## Fase 3 — Studio bootstrap

Crear repositorio/build de Code-OSS:

```text
branding
Open VSX
built-in extension
```

---

## Fase 4 — Connection

La extensión built-in debe:

```text
connect
handshake
reconnect
show status
resolve workspace
```

---

## Fase 5 — Coding workflow

Implementar:

```text
chat
selection
active file
run
cancel
approval
diff
apply edit
terminal
PrAImate Code
```

---

## Fase 6 — Desktop Studio manager

Añadir:

```text
Install
Update
Repair
Open
Recent projects
```

---

## Fase 7 — Full PrAImate frontend

Añadir progresivamente:

```text
Agents
Workflows
Skills
MCP
Models
Tools
Settings
```

---

# 60. Primer milestone concreto

Un milestone razonable sería:

> **Desde PrAImate se instala y abre PrAImate Studio. Studio abre un proyecto, se conecta al runtime de PrAImate, permite iniciar un chat con PrAImate Code, conoce el archivo/selección activos, puede solicitar una modificación y mostrar/aplicar el diff desde el editor.**

Ese milestone valida casi todas las decisiones importantes:

- distribución;
- Code-OSS;
- RPC;
- lifecycle;
- workspace context;
- PrAImate Code;
- chat;
- edits;
- integración nativa.

Si ese flujo funciona bien, el resto del producto puede añadirse encima.

---

# 61. Resultado final esperado

La separación del producto quedaría:

```text
                        PrAImate Core
                             │
            ┌────────────────┼────────────────┐
            │                │                │
            ▼                ▼                ▼
    PrAImate Desktop   PrAImate Studio      CLI/API
                              │
                              ▼
                       PrAImate Code
                       Agents
                       Workflows
                       MCP
                       Skills
                       Models
```

Para usuarios orientados a programación:

```text
PrAImate Studio
```

puede convertirse en la interfaz principal.

Para administración y configuración general:

```text
PrAImate Desktop
```

sigue siendo útil.

Y ambos forman parte del mismo sistema, en lugar de mantener dos implementaciones independientes.

---

# 62. Resumen de la decisión

La implementación más coherente es:

> **Crear PrAImate Studio como una build gestionada de Code-OSS, distribuida desde PrAImate y conectada al mismo backend/runtime mediante una API local versionada.**

La pestaña `Studio` del PrAImate actual sirve para instalar, actualizar, reparar, configurar el arranque y abrir proyectos.

La ventana de Studio es un IDE independiente, pero pertenece al ecosistema de PrAImate y trae integrada de fábrica la interfaz necesaria para usar chats, agentes, workflows, MCP, skills, modelos, approvals, terminales y PrAImate Code.

El punto crítico de la implementación no es el fork visual de Code-OSS.

El punto crítico es crear una **frontera limpia entre PrAImate Core y sus frontends**. Si esa frontera queda bien diseñada, Studio será sostenible. Si Studio empieza a consumir directamente internals, archivos o DB de PrAImate, el mantenimiento se volverá problemático muy rápido.
