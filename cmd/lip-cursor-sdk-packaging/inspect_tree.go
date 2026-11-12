package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// inspectInstallTree checks descendants without traversing symlinks. POSIX mode
// and ownership checks assume the install root's owner and root are trusted;
// the caller must establish that assumption and protect the root's ancestors.
// Windows ACLs are not checked. Links must still resolve within the tree.
func inspectInstallTree(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve install root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve install root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("install root is not an accessible directory")
	}
	rootUID, posix := ownerUID(info)
	if runtime.GOOS != "windows" && !posix {
		return "", fmt.Errorf("install tree ownership is not machine-checkable on this platform")
	}

	err = filepath.WalkDir(canonical, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("cannot enumerate install tree at %q: %w", path, walkErr)
		}
		item, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("cannot inspect install tree entry %q: %w", path, err)
		}
		rel, err := filepath.Rel(canonical, path)
		if err != nil {
			return fmt.Errorf("cannot locate install tree entry: %w", err)
		}
		mode := item.Mode()
		if mode&os.ModeSymlink != 0 {
			if err := resolveContainedLink(canonical, rel); err != nil {
				return fmt.Errorf("install tree link %q %w", rel, err)
			}
			return nil
		}
		if !mode.IsRegular() && !mode.IsDir() {
			return fmt.Errorf("install tree entry %q is not a regular file or directory", rel)
		}
		if !posix {
			return nil
		}
		if mode.Perm()&0o022 != 0 {
			if rel == "." {
				return fmt.Errorf("install root %s is writable beyond its owner (mode 0%03o); reinstall into a protected plugin root", root, mode.Perm())
			}
			return fmt.Errorf("install tree entry %q is writable beyond its owner (mode 0%03o); reinstall into a protected plugin root", rel, mode.Perm())
		}
		uid, ok := ownerUID(item)
		if !ok {
			return fmt.Errorf("install tree entry %q has uncheckable ownership", rel)
		}
		if !trustedTreeOwner(uid, rootUID) {
			return fmt.Errorf("install tree entry %q has owner %d rather than the install root owner or root", rel, uid)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if !posix {
		return "links checked; Windows ACLs not machine-checkable", nil
	}
	return "POSIX modes, ownership and link containment checked (root owner must be trusted)", nil
}

// Resolve each link hop without letting an intermediate path leave the root.
// Checking only the final EvalSymlinks result would permit an attacker-controlled
// external link to point back inside the tree during verification and change later.
func resolveContainedLink(root, rel string) error {
	split := func(value string) []string {
		return strings.FieldsFunc(value, func(r rune) bool {
			return r == '/' || (runtime.GOOS == "windows" && r == '\\')
		})
	}
	pending := split(rel)
	current := root
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case ".", "":
			continue
		case "..":
			if current == root {
				return fmt.Errorf("escapes the protected plugin root")
			}
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			return fmt.Errorf("is dangling or cannot be resolved: %w", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		links++
		if links > 255 {
			return fmt.Errorf("is dangling or cannot be resolved: too many links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return fmt.Errorf("is dangling or cannot be resolved: %w", err)
		}
		if filepath.IsAbs(target) {
			prefix := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
			matchesRoot := target == root
			matchesPrefix := strings.HasPrefix(target, prefix)
			if runtime.GOOS == "windows" {
				matchesRoot = strings.EqualFold(target, root)
				matchesPrefix = len(target) >= len(prefix) && strings.EqualFold(target[:len(prefix)], prefix)
			}
			if !matchesRoot && !matchesPrefix {
				return fmt.Errorf("escapes the protected plugin root")
			}
			if matchesRoot {
				target = ""
			} else {
				target = target[len(prefix):]
			}
			current = root
		}
		pending = append(split(target), pending...)
	}
	return nil
}

func trustedTreeOwner(uid, rootUID uint32) bool {
	return uid == rootUID || uid == 0
}

func runInspectTree(args []string) error {
	flags := newFlagSet("inspect-tree")
	root := flags.String("root", "", "install root to inspect")
	if err := flags.Parse(args); err != nil {
		return reportError("inspect-tree", err)
	}
	if *root == "" || flags.NArg() != 0 {
		return reportError("inspect-tree", fmt.Errorf("-root is required"))
	}
	result, err := inspectInstallTree(*root)
	if err != nil {
		return reportError("inspect-tree", err)
	}
	fmt.Println(result)
	return nil
}
