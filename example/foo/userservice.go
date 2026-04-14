package foo

import (
	"context"
	"fmt"

	"github.com/GiGurra/rewire/example/bar"
)

// User is a simple domain type used to demonstrate rewire.NewMock
// with a generic interface (bar.Repository[User]).
type User struct {
	ID   int
	Name string
}

// UserService is a typical service-layer struct that depends on a
// generic repository interface. In production it would be wired with
// a real database-backed Repository[User]; in tests the dependency is
// satisfied by a rewire.NewMock[bar.Repository[User]].
//
// This is intentionally the kind of code that's annoying to test
// without good mocking support — there's no way to call UserService's
// methods without providing a Repository implementation, and writing
// a hand-rolled mock for every test is exactly the boilerplate that
// rewire's interface mocks eliminate.
type UserService struct {
	repo bar.Repository[User]
}

func NewUserService(repo bar.Repository[User]) *UserService {
	return &UserService{repo: repo}
}

// GetByID fetches a single user from the underlying repository.
func (s *UserService) GetByID(ctx context.Context, id int) (User, error) {
	return s.repo.Get(ctx, id)
}

// AllNames lists every user and returns just their names. Demonstrates
// the slice-of-T return-type case for the generic interface.
func (s *UserService) AllNames(ctx context.Context) ([]string, error) {
	users, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	return names, nil
}

// Rename fetches a user, mutates one field, and saves it back.
// Exercises two methods on the same mock in a single call.
func (s *UserService) Rename(ctx context.Context, id int, newName string) error {
	user, err := s.repo.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("fetching user %d: %w", id, err)
	}
	user.Name = newName
	if err := s.repo.Save(ctx, user); err != nil {
		return fmt.Errorf("saving user %d: %w", id, err)
	}
	return nil
}

// DeleteIfExists deletes the user if Get succeeds, no-ops if Get
// returns an error. Used to exercise three repository methods plus
// error propagation from Get.
func (s *UserService) DeleteIfExists(ctx context.Context, id int) (deleted bool, err error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return false, nil
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return false, fmt.Errorf("deleting user %d: %w", id, err)
	}
	return true, nil
}
