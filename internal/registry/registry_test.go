package registry_test

import (
	"sync"
	"testing"

	"gobot/internal/registry"

	"github.com/bwmarrin/discordgo"
)

func newRegistry() *registry.Registry {
	return &registry.Registry{}
}

func TestRegisterCommand(t *testing.T) {
	r := newRegistry()

	err := r.RegisterCommand(&registry.Command{
		Module: "Test",
		Data:   &discordgo.ApplicationCommand{Name: "test", Description: "test cmd"},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.Commands()) != 1 {
		t.Fatalf("expected 1 command, got %d", len(r.Commands()))
	}
}

func TestRegisterCommand_EmptyName(t *testing.T) {
	r := newRegistry()

	err := r.RegisterCommand(&registry.Command{
		Module:  "Test",
		Data:    &discordgo.ApplicationCommand{Name: ""},
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {},
	})

	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func TestRegisterCommand_NilExecute(t *testing.T) {
	r := newRegistry()

	err := r.RegisterCommand(&registry.Command{
		Module:  "Test",
		Data:    &discordgo.ApplicationCommand{Name: "test"},
		Execute: nil,
	})

	if err == nil {
		t.Fatal("expected error for nil Execute, got nil")
	}
}

func TestRegisterCommand_NilData(t *testing.T) {
	r := newRegistry()

	err := r.RegisterCommand(&registry.Command{
		Module:  "Test",
		Data:    nil,
		Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {},
	})

	if err == nil {
		t.Fatal("expected error for nil Data, got nil")
	}
}

func TestRegisterPrefixCommand(t *testing.T) {
	r := newRegistry()

	err := r.RegisterPrefixCommand(&registry.PrefixCommand{
		Module:  "Test",
		Name:    "test",
		Execute: func(s *discordgo.Session, m *discordgo.MessageCreate) {},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.PrefixCommands()) != 1 {
		t.Fatalf("expected 1 prefix command, got %d", len(r.PrefixCommands()))
	}
}

func TestRegisterPrefixCommand_EmptyName(t *testing.T) {
	r := newRegistry()

	err := r.RegisterPrefixCommand(&registry.PrefixCommand{
		Module:  "Test",
		Name:    "",
		Execute: func(s *discordgo.Session, m *discordgo.MessageCreate) {},
	})

	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func TestRegisterPrefixCommand_NilExecute(t *testing.T) {
	r := newRegistry()

	err := r.RegisterPrefixCommand(&registry.PrefixCommand{
		Module:  "Test",
		Name:    "test",
		Execute: nil,
	})

	if err == nil {
		t.Fatal("expected error for nil Execute, got nil")
	}
}

func TestRegisterModule(t *testing.T) {
	r := newRegistry()

	err := r.RegisterModule(&registry.Module{Name: "Test"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(r.Modules()) != 1 {
		t.Fatalf("expected 1 module, got %d", len(r.Modules()))
	}
}

func TestRegisterModule_EmptyName(t *testing.T) {
	r := newRegistry()

	err := r.RegisterModule(&registry.Module{Name: ""})

	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	r := newRegistry()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = r.RegisterCommand(&registry.Command{
				Module: "Test",
				Data: &discordgo.ApplicationCommand{
					Name:        "cmd",
					Description: "concurrent test",
				},
				Execute: func(s *discordgo.Session, i *discordgo.InteractionCreate) {},
			})
			_ = r.RegisterModule(&registry.Module{Name: "mod"})
			_ = r.Commands()
			_ = r.Modules()
		}(i)
	}
	wg.Wait()
}
