// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

package firmwareimage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const storeOwner = "nco-firmware-image-store-v1\n"

func prepareStoreRoot(root string) (_ string, _ *os.File, err error) {
	if root == "" {
		return "", nil, errors.New("firmware image cache root is required")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return "", nil, errors.New("firmware image cache root must be a real directory")
	}
	cacheRoot := filepath.Join(root, "store-v1")
	if err = os.Mkdir(cacheRoot, 0700); err != nil && !os.IsExist(err) {
		return "", nil, err
	}
	info, err = os.Lstat(cacheRoot)
	if err != nil || !info.IsDir() {
		return "", nil, errors.New("firmware image store directory must not be a symlink")
	}
	fd, err := unix.Open(filepath.Join(cacheRoot, ".owner"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return "", nil, err
	}
	owner := os.NewFile(uintptr(fd), "firmware image cache owner")
	defer func() {
		if err != nil {
			_ = owner.Close()
		}
	}()
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return "", nil, fmt.Errorf("firmware image cache is already owned: %w", err)
	}
	info, err = owner.Stat()
	if err != nil || !info.Mode().IsRegular() {
		err = errors.New("firmware image cache owner must be a regular file")
		return "", nil, err
	}
	marker, err := io.ReadAll(io.LimitReader(owner, int64(len(storeOwner)+1)))
	if err != nil {
		return "", nil, err
	}
	children, err := os.ReadDir(cacheRoot)
	if err != nil {
		return "", nil, err
	}
	if len(marker) == 0 && len(children) == 1 {
		if _, err = owner.WriteAt([]byte(storeOwner), 0); err != nil {
			return "", nil, err
		}
		if err = owner.Sync(); err != nil {
			return "", nil, err
		}
	} else if string(marker) != storeOwner {
		err = errors.New("refusing to clear firmware image cache without its ownership marker")
		return "", nil, err
	}
	for _, child := range children {
		if child.Name() != ".owner" {
			if err = os.RemoveAll(filepath.Join(cacheRoot, child.Name())); err != nil {
				return "", nil, err
			}
		}
	}
	return cacheRoot, owner, nil
}
