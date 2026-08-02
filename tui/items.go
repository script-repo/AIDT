package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// modelItem is a row in the Models list.
type modelItem struct {
	name      string
	family    string
	params    string
	quant     string
	size      string
	isDefault bool
}

func (i modelItem) Title() string {
	if i.isDefault {
		return "★ " + i.name + "  (default)"
	}
	return i.name
}
func (i modelItem) Description() string {
	return fmt.Sprintf("%s · %s · %s · %s",
		dashIf(i.family), dashIf(i.params), dashIf(i.quant), dashIf(i.size))
}
func (i modelItem) FilterValue() string { return i.name }

// poolItem is a row in the Pool (endpoints) list.
type poolItem struct {
	name    string
	status  string
	models  int
	prio    int
	reqs    int
	conns   int
	latency string
	image   string
}

func (i poolItem) Title() string { return i.name }
func (i poolItem) Description() string {
	return fmt.Sprintf("%s · img %s · %d models · prio %d · %d req · %d conns · %s",
		i.status, dashIf(i.image), i.models, i.prio, i.reqs, i.conns, dashIf(i.latency))
}
func (i poolItem) FilterValue() string { return i.name }

// vmItem is a row in the Nutanix VM list.
type vmItem struct {
	name  string
	role  string
	power string
	ip    string
	vcpu  int
	mem   float64
	disk  float64
	image string
}

func (i vmItem) Title() string { return i.name }
func (i vmItem) Description() string {
	return fmt.Sprintf("%s · %s · %s · %dvCPU %.0fGi mem %.0fGi disk · img %s",
		i.role, i.power, i.ip, i.vcpu, i.mem, i.disk, dashIf(i.image))
}
func (i vmItem) FilterValue() string { return i.name }

// agentItem is a row in the Agents list.
type agentItem struct {
	name       string
	cli        string
	target     string
	endpoint   string
	desc       string
	canDeploy  bool
	registered bool
	regHost    string
	regCount   int
}

func (i agentItem) Title() string {
	if i.registered {
		return "✓ " + i.name + "  (registered)"
	}
	return "⬡ " + i.name
}
func (i agentItem) Description() string {
	if i.registered {
		if i.regCount > 1 {
			return fmt.Sprintf("%s · %s · deployed on %d hosts (primary %s)", i.desc, i.endpoint, i.regCount, i.regHost)
		}
		return fmt.Sprintf("%s · %s · deployed on %s", i.desc, i.endpoint, i.regHost)
	}
	state := "preinstalled"
	if i.canDeploy {
		state = "deployable"
	}
	return fmt.Sprintf("%s · %s · %s · %s", i.desc, i.target, i.endpoint, state)
}
func (i agentItem) FilterValue() string { return i.name }

// appItem is a row in the App Deploy list.
type appItem struct {
	name     string
	desc     string
	kind     string   // "helm" | "manifest"
	source   string   // chart ref or manifest URL, for the description
	contexts []string // clusters this app is installed on
	count    int      // recorded installations
	missing  int      // installations the last refresh could not find
	add      bool     // the "+ add application" row
}

func (i appItem) Title() string {
	if i.add {
		return "+ add application"
	}
	switch {
	case i.count == 0:
		return "⬡ " + i.name
	case i.missing > 0:
		return "⚠ " + i.name + "  (deployed, " + plural(i.missing, "missing", "missing") + ")"
	default:
		return "✓ " + i.name + "  (deployed)"
	}
}

func (i appItem) Description() string {
	if i.add {
		return "define a new chart or manifest to deploy"
	}
	if i.count == 0 {
		return fmt.Sprintf("%s · %s · %s · not deployed", dashIf(i.desc), i.kind, dashIf(i.source))
	}
	where := strings.Join(i.contexts, ", ")
	if i.count > len(i.contexts) {
		where = fmt.Sprintf("%s (%d installs)", where, i.count)
	}
	return fmt.Sprintf("%s · %s · on %s", dashIf(i.desc), i.kind, where)
}

func (i appItem) FilterValue() string { return i.name + " " + i.desc }

// stateColor drives the list colouring: an app that is deployed reads
// differently at a glance from one that is not, which is the whole point of the
// section. Yellow is reserved for a registration whose workload has gone away.
func (i appItem) stateColor() lipgloss.Color {
	switch {
	case i.add:
		return colCyan
	case i.missing > 0:
		return colYellow
	case i.count > 0:
		return colGreen
	default:
		return colMuted
	}
}

// k8sItem is a row in the K8S list: one context from the gateway's kubeconfig,
// or the "+ add cluster" action.
type k8sItem struct {
	name    string
	cluster string
	server  string
	apps    int
	current bool
	add     bool
}

func (i k8sItem) Title() string {
	if i.add {
		return "+ add cluster"
	}
	if i.current {
		return "★ " + i.name + "  (current)"
	}
	return "  " + i.name
}

func (i k8sItem) Description() string {
	if i.add {
		return "import a kubeconfig, or pull one from a MicroK8s node"
	}
	apps := "no apps"
	if i.apps > 0 {
		apps = plural(i.apps, "app", "apps")
	}
	return fmt.Sprintf("%s · %s · %s", dashIf(i.server), dashIf(i.cluster), apps)
}

func (i k8sItem) FilterValue() string { return i.name + " " + i.cluster + " " + i.server }

func (i k8sItem) stateColor() lipgloss.Color {
	switch {
	case i.add:
		return colCyan
	case i.current:
		return colGreen
	default:
		return colText
	}
}

// coloredItem is a list row that picks its own colour from its state.
type coloredItem interface {
	list.Item
	Title() string
	Description() string
	stateColor() lipgloss.Color
}

// stateDelegate renders two-line rows whose colour comes from the item.
//
// bubbles' DefaultDelegate styles every row identically, so it cannot express
// "deployed apps are a different colour". This keeps the same shape and
// selection affordance as the default delegate and only takes over the colour.
type stateDelegate struct{}

func (stateDelegate) Height() int                         { return 2 }
func (stateDelegate) Spacing() int                        { return 1 }
func (stateDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d stateDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(coloredItem)
	if !ok {
		return
	}
	// Leave room for the two-cell gutter so a long title cannot wrap and break
	// the fixed row height the delegate promises.
	width := m.Width() - 2
	if width < 1 {
		width = 1
	}
	title, desc := it.Title(), it.Description()

	selected := index == m.Index()
	gutter := "  "
	titleStyle := lipgloss.NewStyle().Foreground(it.stateColor()).MaxWidth(width)
	descStyle := lipgloss.NewStyle().Foreground(colMuted).MaxWidth(width)
	if selected {
		gutter = lipgloss.NewStyle().Foreground(colAccent).Render("│ ")
		// The selection has to stay legible against every state colour, so it
		// is signalled by the bar and a bold title rather than by recolouring.
		titleStyle = titleStyle.Bold(true)
		descStyle = descStyle.Foreground(colPrimary)
	}
	fmt.Fprintf(w, "%s%s\n%s%s", gutter, titleStyle.Render(title), gutter, descStyle.Render(desc))
}

// plural renders "1 app" / "3 apps".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func dashIf(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
