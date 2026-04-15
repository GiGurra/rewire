package foo

import (
	"context"
	"errors"
	"testing"

	"github.com/GiGurra/rewire/example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// Production-flow style: mock the generic Repository interface, hand
// it to UserService via NewUserService, and exercise UserService's
// methods. The mock satisfies bar.Repository[User] at compile time
// AND at runtime, so production code never knows it's running against
// a stub.
//
// This is the test pattern most users will write. The mock is set up
// per test, the production code is unmodified, and the rewire APIs
// stay out of the assertions.

func TestUserService_GetByID_Found(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)
	rewire.InstanceFunc(t, repo, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			if id != 42 {
				t.Errorf("unexpected id: got %d, want 42", id)
			}
			return User{ID: 42, Name: "Alice"}, nil
		})

	svc := NewUserService(repo)
	user, err := svc.GetByID(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != 42 || user.Name != "Alice" {
		t.Errorf("got %+v, want User{ID: 42, Name: Alice}", user)
	}
}

func TestUserService_GetByID_NotFound(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)
	rewire.InstanceFunc(t, repo, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{}, errors.New("not found")
		})

	svc := NewUserService(repo)
	_, err := svc.GetByID(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUserService_AllNames(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)
	rewire.InstanceFunc(t, repo, bar.Repository[User].List,
		func(r bar.Repository[User], ctx context.Context) ([]User, error) {
			return []User{
				{ID: 1, Name: "Alice"},
				{ID: 2, Name: "Bob"},
				{ID: 3, Name: "Charlie"},
			}, nil
		})

	svc := NewUserService(repo)
	names, err := svc.AllNames(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Alice", "Bob", "Charlie"}
	if len(names) != len(want) {
		t.Fatalf("got %d names, want %d", len(names), len(want))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestUserService_AllNames_Error(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)
	rewire.InstanceFunc(t, repo, bar.Repository[User].List,
		func(r bar.Repository[User], ctx context.Context) ([]User, error) {
			return nil, errors.New("db down")
		})

	svc := NewUserService(repo)
	_, err := svc.AllNames(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUserService_Rename_GetThenSave(t *testing.T) {
	// Two methods on the same mock — Get is called first, then Save
	// with the modified result. Verifies that multiple per-instance
	// stubs on the same mock work together.
	repo := rewire.NewMock[bar.Repository[User]](t)

	var savedUser User
	rewire.InstanceFunc(t, repo, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{ID: id, Name: "OldName"}, nil
		})
	rewire.InstanceFunc(t, repo, bar.Repository[User].Save,
		func(r bar.Repository[User], ctx context.Context, item User) error {
			savedUser = item
			return nil
		})

	svc := NewUserService(repo)
	if err := svc.Rename(context.Background(), 7, "NewName"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if savedUser.ID != 7 {
		t.Errorf("savedUser.ID = %d, want 7", savedUser.ID)
	}
	if savedUser.Name != "NewName" {
		t.Errorf("savedUser.Name = %q, want NewName", savedUser.Name)
	}
}

func TestUserService_Rename_GetFails(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)

	saveCalled := false
	rewire.InstanceFunc(t, repo, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{}, errors.New("not found")
		})
	rewire.InstanceFunc(t, repo, bar.Repository[User].Save,
		func(r bar.Repository[User], ctx context.Context, item User) error {
			saveCalled = true
			return nil
		})

	svc := NewUserService(repo)
	if err := svc.Rename(context.Background(), 7, "NewName"); err == nil {
		t.Fatal("expected error, got nil")
	}
	if saveCalled {
		t.Error("Save should not be called when Get fails")
	}
}

func TestUserService_DeleteIfExists_Exists(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)

	var deletedID int
	rewire.InstanceFunc(t, repo, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{ID: id, Name: "Existing"}, nil
		})
	rewire.InstanceFunc(t, repo, bar.Repository[User].Delete,
		func(r bar.Repository[User], ctx context.Context, id int) error {
			deletedID = id
			return nil
		})

	svc := NewUserService(repo)
	deleted, err := svc.DeleteIfExists(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}
	if deletedID != 5 {
		t.Errorf("deletedID = %d, want 5", deletedID)
	}
}

func TestUserService_DeleteIfExists_NotFound(t *testing.T) {
	repo := rewire.NewMock[bar.Repository[User]](t)

	deleteCalled := false
	rewire.InstanceFunc(t, repo, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{}, errors.New("not found")
		})
	rewire.InstanceFunc(t, repo, bar.Repository[User].Delete,
		func(r bar.Repository[User], ctx context.Context, id int) error {
			deleteCalled = true
			return nil
		})

	svc := NewUserService(repo)
	deleted, err := svc.DeleteIfExists(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted {
		t.Error("expected deleted=false")
	}
	if deleteCalled {
		t.Error("Delete should not be called when Get fails")
	}
}

// Two services backed by separate mocks of the SAME generic interface
// instantiation must remain independent. Stubs on one don't bleed into
// the other.
func TestUserService_TwoServicesIsolated(t *testing.T) {
	repo1 := rewire.NewMock[bar.Repository[User]](t)
	repo2 := rewire.NewMock[bar.Repository[User]](t)

	rewire.InstanceFunc(t, repo1, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{ID: id, Name: "FromRepo1"}, nil
		})
	rewire.InstanceFunc(t, repo2, bar.Repository[User].Get,
		func(r bar.Repository[User], ctx context.Context, id int) (User, error) {
			return User{ID: id, Name: "FromRepo2"}, nil
		})

	svc1 := NewUserService(repo1)
	svc2 := NewUserService(repo2)

	u1, _ := svc1.GetByID(context.Background(), 1)
	u2, _ := svc2.GetByID(context.Background(), 1)

	if u1.Name != "FromRepo1" {
		t.Errorf("svc1 saw %q, want FromRepo1", u1.Name)
	}
	if u2.Name != "FromRepo2" {
		t.Errorf("svc2 saw %q, want FromRepo2", u2.Name)
	}
}
