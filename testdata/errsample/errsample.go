// Package errsample exercises error-construction extraction. Parsed by
// tests, never compiled.
package errsample

func demo() error {
	if err != nil {
		return fmt.Errorf("load user %d: %w", id, err)
	}
	if notFound {
		return errors.New("user not found")
	}
	if err := open(); err != nil {
		return errors.Wrapf(err, "open %s", path)
	}
	if missing {
		return status.Errorf(codes.NotFound, "no dataset %s", name)
	}
	// fully dynamic: counted, no template
	return errors.New(msg)
}
