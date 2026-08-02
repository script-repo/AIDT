package main

import "github.com/charmbracelet/bubbles/key"

// keyMap holds every binding the TUI understands. The help bar (bubbles/help)
// renders a contextual subset of these depending on focus zone and section.
type keyMap struct {
	Up   key.Binding
	Down key.Binding
	Open key.Binding
	Back key.Binding

	Connect    key.Binding
	Disconnect key.Binding
	Refresh    key.Binding

	Add      key.Binding
	Remove   key.Binding
	Pull     key.Binding
	Browse   key.Binding
	OpenLink key.Binding
	SetDef   key.Binding
	SetModel key.Binding
	Filter   key.Binding

	Console   key.Binding
	ConsoleGW key.Binding

	Deploy       key.Binding
	Worker       key.Binding
	Custom       key.Binding
	CustomWorker key.Binding
	OllaLocal    key.Binding
	NextName     key.Binding
	Delete       key.Binding
	EditCfg      key.Binding

	Token      key.Binding
	ClearToken key.Binding

	AgentOpen     key.Binding
	AgentDeploy   key.Binding
	ServiceOpen   key.Binding
	ServiceForget key.Binding

	AppDeploy  key.Binding
	AppRemove  key.Binding
	AppAdd     key.Binding
	AppEdit    key.Binding
	AppForget  key.Binding
	K8sUse     key.Binding
	K8sAdd     key.Binding
	K8sRemove  key.Binding
	K8sRefresh key.Binding

	Send       key.Binding
	NewSession key.Binding

	Help key.Binding
	Quit key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:           key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:         key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Open:         key.NewBinding(key.WithKeys("enter", "right", "l"), key.WithHelp("enter", "open")),
		Back:         key.NewBinding(key.WithKeys("esc", "left", "h"), key.WithHelp("esc", "menu")),
		Connect:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "connect")),
		Disconnect:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "disconnect")),
		Refresh:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Add:          key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add endpoint")),
		Remove:       key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
		Pull:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pull model")),
		Browse:       key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "browse/download")),
		OpenLink:     key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "open last link")),
		SetDef:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "set default")),
		SetModel:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "set chat model")),
		Filter:       key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Console:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "ssh console")),
		ConsoleGW:    key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "ssh gateway")),
		Deploy:       key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "deploy gateway")),
		Worker:       key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "deploy worker")),
		Custom:       key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "custom deployments")),
		CustomWorker: key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "deploy on worker")),
		OllaLocal:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "install Olla here")),
		NextName:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next name")),
		Delete:       key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete VM")),
		EditCfg:      key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "settings")),
		Token:        key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "new token")),
		ClearToken:   key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "clear token")),
		AgentOpen:    key.NewBinding(key.WithKeys("o", "enter"), key.WithHelp("o/enter", "open agent")),
		AgentDeploy:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deploy agent")),
		ServiceOpen:  key.NewBinding(key.WithKeys("enter", "b"), key.WithHelp("enter/b", "open URL")),
		// Distinct from Delete ("delete VM"): this only forgets a listing, it
		// never touches the workload the listing points at.
		ServiceForget: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove listing")),
		AppDeploy:     key.NewBinding(key.WithKeys("d", "enter"), key.WithHelp("d", "deploy app")),
		// Unlike ServiceForget, this really uninstalls the workload — the same
		// meaning x carries in the Agents section.
		AppRemove:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove from cluster")),
		AppAdd:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add app")),
		AppEdit:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit app")),
		AppForget:  key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "drop from catalog")),
		K8sUse:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "set current")),
		K8sAdd:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add cluster")),
		K8sRemove:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove entry")),
		K8sRefresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Send:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		NewSession: key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "new session")),
		Help:       key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}
