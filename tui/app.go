package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

const pollInterval = 2 * time.Second

// ---- sections (left-hand navigation) ---------------------------------------

type section int

const (
	secDash section = iota
	secPool
	secModels
	secChat
	secAgents
	secLoad
	secNutanix
	secServices
	secAccess
	secUpdate
	// App Deploy and K8S are appended rather than grouped next to Nutanix so the
	// ten sections that predate them keep the number shortcuts operators already
	// know (see the quick-jump handler in update.go).
	secApps
	secAppSvcs
	secK8s
)

type sectionInfo struct {
	name string
	hint string
}

var sections = []sectionInfo{
	{"Dashboard", "live metrics & health"},
	{"Pool", "ollama endpoints"},
	{"Models", "models in the pool"},
	{"Chat", "test the pool"},
	{"Agents", "CLI agents via ssh"},
	{"Load", "load balancing"},
	{"Nutanix", "VMs & deploy"},
	{"Services", "direct service URLs"},
	{"Access", "base URL & token"},
	{"Update", "maintenance & upgrades"},
	{"App Deploy", "apps on kubernetes"},
	{"App Services", "deployed app URLs"},
	{"K8S", "kubeconfig clusters"},
}

// numberedSections is how many sections the 1-9/0 quick-jump can reach. There
// are only ten keys, so anything past this is reached from the sidebar.
const numberedSections = 10

// focus zone: are we navigating the sidebar, or interacting with the content?
type zone int

const (
	zoneSidebar zone = iota
	zoneContent
)

type modalKind int

const (
	modalNone modalKind = iota
	modalConnect
	modalEndpoint
	modalPull
	modalDeploy
	modalCatalog
	modalNutanixCfg
	modalAgentHost
	modalHermesCfg
	modalUpdateImage
	modalOSUpdate
	modalOllaKey
	modalUpdateAll
	modalCustomDeploy
	modalCustomWorker
	modalAgentRemove
	modalAppDeploy
	modalAppRemove
	modalAppAdd
	modalK8sAdd
)

type chatRole int

const (
	roleUser chatRole = iota
	roleBot
)

type chatTurn struct {
	role    chatRole
	content string
}

type model struct {
	// layout
	width, height int
	contentW      int // usable inner width of the content card
	contentH      int // usable inner height of the content card
	ready         bool

	// navigation
	section section
	zone    zone

	// connection
	gateway   string
	connected bool
	client    *OllaClient
	connInfo  string
	connVer   string // last known "Name Version Edition" banner, for reconnects
	sshUser   string
	sshPass   string

	// prism central
	pcCfg     *PCConfig
	deployCfg deploySettings // VM template + image used for new deploys
	pcOver    pcOverride     // persisted PC instance/account override (may be empty)

	// data
	status    Status
	models    []Model
	vms       []VM
	clusters  []string        // PC clusters available for placement
	images    []string        // PC disk images available to clone
	subnets   []string        // PC subnets available for placement
	endpoints []endpointEntry // cached gateway endpoints (for direct worker ops/console)
	// managedSig fingerprints the gateway+worker host set the Nutanix list was
	// last built from, so it is rebuilt on membership changes but not on every
	// status poll.
	managedSig string

	// derived metrics
	prevReq    int
	prevBytes  float64
	prevTime   time.Time
	reqPerS    float64
	bytesPerS  float64
	reqHistory []float64
	prevEpReq  map[string]int
	epDelta    map[string]float64
	lastTokS   float64
	lastTTFT   float64

	// charm components
	km   keyMap
	help help.Model
	spin spinner.Model
	glam *glamour.TermRenderer
	prog progress.Model

	modelsList   list.Model
	poolList     list.Model
	vmsList      list.Model
	agentsList   list.Model
	servicesList list.Model
	updateList   list.Model
	customList   list.Model // user-defined custom deployment types (Nutanix submenu)

	appsList   list.Model // App Deploy catalog
	appSvcList list.Model // reachable addresses of deployed apps
	k8sList    list.Model // clusters from the gateway's kubeconfig

	// custom deployment types (Nutanix submenu)
	customDeploys []customDeploy
	services      []serviceLink
	nutanixCustom bool // Nutanix section is showing the custom-deploy submenu

	// App Deploy: the catalog, the seed ledger, and what is installed where.
	apps       []k8sApp
	appsSeeded []string
	appDeploys []appDeployment
	// pendingApp is the installation a running deploy/remove will record or
	// forget when it finishes. Registry writes happen on success only, so a
	// failed deploy never leaves the list claiming the app is there.
	pendingApp     *appDeployment
	pendingAppAct  string // "deploy" | "remove"
	appBusy        bool
	k8sContexts    []k8sContext
	k8sErr         string // last kubeconfig read failure, shown in the K8S view
	k8sLoading     bool
	appEditingName string // catalog entry being edited ("" = adding a new one)

	// App Services: where deployed apps are reachable. Derived from the cluster
	// on refresh and never persisted — an external address is assigned by the
	// cluster and a remembered one would eventually point somewhere wrong.
	appServices   []appService
	appSvcErr     string
	appSvcLoading bool

	// appSecrets holds generated chart values per installation, keyed by
	// appDeployment.secretKey().
	appSecrets map[string]map[string]string

	// custom-deploy access link: pendingCustom tracks an in-flight setup. Its
	// service URL is persisted only after the setup exits successfully.
	pendingCustom    *customRun
	lastCustomAccess string
	lastCustomName   string

	// image attribution: vmImages is the deploy-time VM name -> image map we
	// record; imageByID resolves a VM's source-image extId to a name from PC.
	vmImages  map[string]string
	imageByID map[string]string

	// agent registrations: agentReg holds the preferred open host per agent;
	// agentHosts tracks every gateway or worker where it is installed.
	agentReg          map[string]string
	agentHosts        map[string][]string
	pendingAgent      string   // agent awaiting host pick
	pendingAct        string   // "open" | "deploy"
	pendingAgentHosts []string // worker snapshot used by the all-workers option
	agentBusy         bool     // a multi-worker deployment or removal is running
	agentInstances    int      // container count for the next containerized-agent deploy
	chatVP            viewport.Model
	logVP             viewport.Model
	composer          textarea.Model

	// chat
	chatModel       string
	defModel        string // persisted pool-wide default model
	history         []chatTurn
	partial         string
	streaming       bool
	chatCh          chan ChatEvent
	chatStart       time.Time
	chatFirst       time.Time
	chatTokens      int
	chatTotalTokens int    // prompt+completion from usage (when reported)
	chatModelUsed   string // model of the in-flight chat (for usage attribution)

	// usage ledger (per-model tokens, 30-day rolling aggregate)
	usageAgg map[string]int64

	// pull
	pullCh   chan PullEvent
	pulling  bool
	pullName string
	pullStat string
	pullFrac float64
	// multi-worker parallel pull: one progress row per worker
	multiPull bool
	pullRows  []pullRow

	// proc (deploy/delete)
	procCh             chan ProcEvent
	procBusy           bool
	localOllaPending   bool     // the running proc is a local Olla install; connect on success
	pendingDeleteHosts []string // clear these host aliases after successful VM deletion
	// pendingDeleteVMs holds the names of the VMs being deleted. Custom services
	// deployed onto a new VM are recorded against its name, not its address, so
	// the address list alone cannot find them.
	pendingDeleteVMs []string
	probingLocal     bool        // startup probe for a local Olla is in flight; hold off the Connect form
	batch            deployBatch // active multi-worker parallel deploy, if any
	logLines         []string

	// access
	token   string
	tokFile string

	// modal form (huh)
	modal modalKind
	form  *huh.Form
	// form-bound values
	fGateway string
	fSSHUser string
	fSSHPass string
	fEpName  string
	fEpURL   string
	fEpType  string
	fEpPrio  string
	fRole    string
	fName    string
	fModel   string
	fCount   string
	// nutanix settings form values
	fPCHost  string
	fPCPort  string
	fPCKey   string
	fPCUser  string
	fPCPass  string
	fImage   string
	fCluster string
	fSubnet  string
	fSockets string
	fCores   string
	fMem     string
	fDisk    string
	fVMUser  string
	fVMPass  string
	// agent host picker
	fAgentHost string
	// agent remove picker
	fRemoveTarget string // selected host target for agent removal
	// hermes gateway / telegram settings form values
	fTgToken   string
	fTgAllowed string
	fTgHome    string
	fGwMode    string
	fGwEnable  bool
	// update section form values
	fUpdImage   string
	fSeedName   string
	fSeedURL    string
	fSeedPreset string
	fOSHosts    []string
	fOllaKey    string
	fOllaTarget string
	fUpdConfirm bool
	// custom deployment form values
	fCustName   string
	fCustURL    string
	fCustScheme string
	fCustPort   string
	fCustPath   string
	fCustHost   string
	// App Deploy form values
	fAppCtx      string
	fAppNS       string
	fAppRelease  string
	fAppTarget   string // selected installation, for removal
	fAppName     string
	fAppDesc     string
	fAppRepo     string
	fAppChart    string
	fAppVersion  string
	fAppValues   string
	fAppManifest string
	fAppExpose   string
	// K8S form values
	fK8sSource   string // "microk8s" | "file"
	fK8sPath     string
	fK8sCtxName  string
	fK8sNode     string
	fK8sNodeUser string
	fK8sNodePass string

	// hermes gateway / telegram config (persisted)
	hermesCfg hermesSettings

	// transient status line
	notice string
}

// ---- construction ----------------------------------------------------------

func newModel(gateway, sshUser, sshPass string) model {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(colAccent).BorderForeground(colAccent)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(colPrimary).BorderForeground(colAccent)
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.Foreground(colText)
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.Foreground(colMuted)

	mkList := func(title string) list.Model {
		l := list.New(nil, delegate, 30, 10)
		l.Title = title
		l.SetShowTitle(false)
		l.SetShowHelp(false)
		l.SetStatusBarItemName("item", "items")
		l.Styles.StatusBar = l.Styles.StatusBar.Foreground(colMuted)
		l.Styles.NoItems = l.Styles.NoItems.Foreground(colSubtle)
		return l
	}

	ta := textarea.New()
	ta.Placeholder = "Message the pool…  (Enter to send · Esc to menu)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 4000
	ta.SetHeight(3)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Prompt = lipgloss.NewStyle().Foreground(colAccent).Render("┃ ")

	prog := progress.New(progress.WithDefaultGradient(), progress.WithWidth(40))

	hp := help.New()
	hp.Styles.ShortKey = hp.Styles.ShortKey.Foreground(colCyan)
	hp.Styles.ShortDesc = hp.Styles.ShortDesc.Foreground(colMuted)
	hp.Styles.FullKey = hp.Styles.FullKey.Foreground(colCyan)
	hp.Styles.FullDesc = hp.Styles.FullDesc.Foreground(colMuted)

	home, _ := os.UserHomeDir()
	tokFile := filepath.Join(home, ".ai-deployment-toolkit", "tui.json")
	st := loadSettings(tokFile)

	// Pre-populate built-in custom deployments once. The seeded flag means
	// deleting them all later sticks (they won't silently reappear).
	customDeploys := st.CustomDeploys
	seedCustom := false
	if len(customDeploys) == 0 && !st.CustomSeeded {
		customDeploys = defaultCustomDeploys()
		seedCustom = true
	}
	customDeploys, migrateCustom := migrateBuiltinCustomDeploys(customDeploys)
	// Top up with any built-in added by a newer AIDT release, recording each one
	// so a later delete is not undone on the next launch.
	customDeploys, seededBuiltins, addedBuiltin := seedBuiltinCustomDeploys(customDeploys, st.SeededBuiltins, st.CustomSeeded || seedCustom)
	vmImages := st.VMImages
	if vmImages == nil {
		vmImages = map[string]string{}
	}

	// Seed the App Deploy catalog on the same contract as the custom deploys: a
	// config that predates the catalog counts as already offered, so an operator
	// who curates the list does not get the built-ins back on every upgrade.
	appsLegacySeeded := len(st.Apps) > 0 && len(st.AppsSeeded) == 0
	apps, appsSeeded, appsChanged := seedBuiltinApps(st.Apps, st.AppsSeeded, appsLegacySeeded)

	// Connection details come from flags/env first, then the values captured on
	// a previous launch (first-run setup), then the built-in user fallback.
	gateway = orDefault(gateway, st.Gateway)
	sshUser = orDefault(sshUser, st.SSHUser)
	sshPass = orDefault(sshPass, st.SSHPass)

	// Prism Central comes from the persisted override when set, else mcp.json.
	pcCfg := pcConfigFromOverride(st.PC)
	if pcCfg == nil {
		pcCfg = LoadPCConfig()
	}

	// Strip any old hardcoded lab placement (e.g. "canucks") carried over in
	// tui.json so it never shows as a default in a different environment.
	cleanedDeploy := dropLegacyPlacement(st.Deploy)
	if cleanedDeploy != st.Deploy {
		_ = saveDeployPC(tokFile, withDeployDefaults(cleanedDeploy), st.PC)
	}

	m := model{
		gateway:       normalizeGateway(gateway),
		sshUser:       orDefault(sshUser, "rocky"),
		sshPass:       sshPass,
		pcCfg:         pcCfg,
		deployCfg:     withDeployDefaults(cleanedDeploy),
		pcOver:        st.PC,
		km:            newKeyMap(),
		help:          hp,
		spin:          sp,
		glam:          newGlamour(80),
		prog:          prog,
		modelsList:    mkList("Models"),
		poolList:      mkList("Pool"),
		vmsList:       mkList("VMs"),
		agentsList:    mkList("Agents"),
		servicesList:  mkList("Services"),
		updateList:    mkList("Update"),
		customList:    mkList("Custom deployments"),
		appsList:      mkList("App Deploy"),
		appSvcList:    mkList("App Services"),
		k8sList:       mkList("K8S"),
		chatVP:        viewport.New(80, 16),
		logVP:         viewport.New(80, 8),
		composer:      ta,
		prevEpReq:     map[string]int{},
		epDelta:       map[string]float64{},
		tokFile:       tokFile,
		token:         st.Token,
		defModel:      st.DefaultModel,
		usageAgg:      usage30(loadUsage(usagePath(tokFile))),
		agentReg:      st.Agents,
		agentHosts:    st.AgentHosts,
		hermesCfg:     st.Hermes,
		customDeploys: customDeploys,
		services:      st.Services,
		vmImages:      vmImages,
		imageByID:     map[string]string{},
		apps:          apps,
		appsSeeded:    appsSeeded,
		appDeploys:    st.AppDeploys,
		appSecrets:    st.AppSecrets,
	}
	if seedCustom || migrateCustom || addedBuiltin {
		_ = saveCustomDeploys(tokFile, customDeploys, seededBuiltins)
	}
	if appsChanged {
		_ = saveApps(tokFile, apps, appsSeeded)
	}
	if m.agentReg == nil {
		m.agentReg = map[string]string{}
	}
	if m.agentHosts == nil {
		m.agentHosts = map[string][]string{}
	}
	// Migrate configs saved before per-agent host lists existed.
	for a, h := range m.agentReg {
		if h != "" && len(m.agentHosts[a]) == 0 {
			m.agentHosts[a] = []string{h}
		}
	}
	if m.defModel != "" {
		m.chatModel = m.defModel
	}
	// With no gateway configured, Init() probes for a local Olla. Mark that in
	// flight so the first WindowSizeMsg doesn't open the Connect form over a
	// probe that is about to succeed.
	m.probingLocal = m.gateway == ""

	// The Update list is a dense menu (7 fixed actions), so give it a compact
	// single-line delegate — the default 3-line delegate would paginate after
	// ~3 rows in the available height.
	compact := list.NewDefaultDelegate()
	compact.ShowDescription = false
	compact.SetSpacing(0)
	compact.SetHeight(1)
	compact.Styles.SelectedTitle = compact.Styles.SelectedTitle.
		Foreground(colAccent).BorderForeground(colAccent)
	compact.Styles.NormalTitle = compact.Styles.NormalTitle.Foreground(colText)
	m.updateList.SetDelegate(compact)
	m.customList.SetDelegate(compact)

	// App Deploy and K8S colour their rows by state, which the default delegate
	// cannot express (see stateDelegate).
	m.appsList.SetDelegate(stateDelegate{})
	m.appSvcList.SetDelegate(stateDelegate{})
	m.k8sList.SetDelegate(stateDelegate{})

	m.refreshAgents()
	m.refreshUpdateList()
	m.refreshCustomList()
	m.refreshServices()
	m.refreshAppsList()
	m.refreshK8sList()
	return m
}

func newGlamour(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	// Use a fixed dark style rather than WithAutoStyle: auto-style probes the
	// terminal background (an OSC/cursor-position query) every time this runs —
	// including on each window resize — and the reply leaks into focused inputs
	// over SSH / serial consoles.
	g, _ := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(width))
	return g
}

// ---- Bubble Tea: Init ------------------------------------------------------

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick, tickCmd()}
	if m.pcCfg != nil {
		cmds = append(cmds, vmsCmd(m.pcCfg))
	}
	if m.gateway != "" {
		cmds = append(cmds, connectCmd(m.gateway))
	} else {
		// Nothing configured yet. The TUI is often run on the gateway box
		// itself (it ships there with the deploy), so look for an Olla on this
		// machine before asking anyone to type a URL. The probe falls back to
		// firstRunMsg, which opens the Connect form as before.
		cmds = append(cmds, localOllaProbeCmd())
	}
	return tea.Batch(cmds...)
}

// ---- messages --------------------------------------------------------------

type tickMsg time.Time

// firstRunMsg is emitted once at startup when no gateway is configured and no
// local Olla was found, so the connection modal opens automatically for the
// user to enter their details.
type firstRunMsg struct{}

// localOllaFoundMsg reports that the startup probe found an Olla gateway
// running on this machine, so the TUI can connect to it without being
// configured by hand.
type localOllaFoundMsg struct{ gateway string }
type connectedMsg struct {
	gateway string
	info    VersionInfo
	err     error
}
type statusMsg struct {
	st  Status
	err error
}
type modelsMsg struct {
	models []Model
	err    error
}
type vmsMsg struct {
	vms          []VM
	clusters     []string
	images       []string
	subnets      []string
	imageByID    map[string]string // image extId -> name
	err          error
	placementErr error // non-nil if the cluster/image/subnet queries failed
}
type chatEvMsg ChatEvent
type pullEvMsg PullEvent

// pullRow tracks one worker's progress during a parallel multi-worker download.
type pullRow struct {
	worker string
	stat   string
	frac   float64
	done   bool
	failed bool
}
type procEvMsg ProcEvent
type nextNameMsg struct {
	name string
	err  error
}
type sshResultMsg struct {
	msg string
	err error
}
type endpointsMsg struct {
	eps []endpointEntry
	err error
}

// batchEndpoint is a worker endpoint emitted (as "AIDT_ENDPOINT <json>") by a
// parallel `pattern-b --no-register` run, collected for batched registration.
type batchEndpoint struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Type     string `json:"type"`
	Priority int    `json:"priority"`
}

// deployBatch tracks a multi-worker parallel deploy: phase 1 provisions N workers
// concurrently (collecting their endpoints), phase 2 registers them all with the
// gateway in a single olla.yaml write to avoid races.
type deployBatch struct {
	active    bool
	phase     int // 1 = provisioning, 2 = registering
	gateway   string
	vmUser    string
	vmPass    string
	total     int
	endpoints []batchEndpoint
}
type consoleReadyMsg struct {
	user  string
	host  string
	key   string
	cmd   string // remote command to launch (empty = interactive login shell)
	label string
	agent string // non-empty: register this agent on clean exit
	local bool   // run cmd on this machine instead of over SSH
	err   error
}
type agentRegisteredMsg struct {
	agent string
	host  string
}
type agentBatchDeployedMsg struct {
	agent       string
	okHosts     []string
	errs        []string
	gatewayHost string
}
type notifyMsg string

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}
