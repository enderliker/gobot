package registry

import (
	"fmt"
	"sync"

	"github.com/bwmarrin/discordgo"
)

type Command struct {
	Module  string
	Data    *discordgo.ApplicationCommand
	Execute func(*discordgo.Session, *discordgo.InteractionCreate)
}

type PrefixCommand struct {
	Module  string
	Name    string
	Execute func(*discordgo.Session, *discordgo.MessageCreate)
}

type Module struct {
	Name string
}

type Registry struct {
	mu             sync.RWMutex
	commands       []*Command
	prefixCommands []*PrefixCommand
	modules        []*Module
}

func (r *Registry) RegisterCommand(c *Command) error {
	if c.Data == nil || c.Data.Name == "" {
		return fmt.Errorf("command must have a name")
	}
	if c.Execute == nil {
		return fmt.Errorf("command %q must have an Execute function", c.Data.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, c)
	return nil
}

func (r *Registry) RegisterPrefixCommand(c *PrefixCommand) error {
	if c.Name == "" {
		return fmt.Errorf("prefix command must have a name")
	}
	if c.Execute == nil {
		return fmt.Errorf("prefix command %q must have an Execute function", c.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.prefixCommands = append(r.prefixCommands, c)
	return nil
}

func (r *Registry) RegisterModule(m *Module) error {
	if m.Name == "" {
		return fmt.Errorf("module must have a name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules = append(r.modules, m)
	return nil
}

func (r *Registry) Commands() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commands
}

func (r *Registry) PrefixCommands() []*PrefixCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.prefixCommands
}

func (r *Registry) Modules() []*Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.modules
}

var Default = &Registry{}

func RegisterCommand(c *Command) error {
	return Default.RegisterCommand(c)
}

func RegisterPrefixCommand(c *PrefixCommand) error {
	return Default.RegisterPrefixCommand(c)
}

func RegisterModule(m *Module) error {
	return Default.RegisterModule(m)
}
