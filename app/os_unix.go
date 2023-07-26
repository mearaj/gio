// SPDX-License-Identifier: Unlicense OR MIT

//go:build (linux && !android) || freebsd || openbsd
// +build linux,!android freebsd openbsd

package app

import (
	"errors"
	"fmt"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	syscall "golang.org/x/sys/unix"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unsafe"
)

// ViewEvent provides handles to the underlying window objects for the
// current display protocol.
type ViewEvent interface {
	implementsViewEvent()
	ImplementsEvent()
}

type X11ViewEvent struct {
	// Display is a pointer to the X11 Display created by XOpenDisplay.
	Display unsafe.Pointer
	// Window is the X11 window ID as returned by XCreateWindow.
	Window uintptr
}

func (X11ViewEvent) implementsViewEvent() {}
func (X11ViewEvent) ImplementsEvent()     {}

type WaylandViewEvent struct {
	// Display is the *wl_display returned by wl_display_connect.
	Display unsafe.Pointer
	// Surface is the *wl_surface returned by wl_compositor_create_surface.
	Surface unsafe.Pointer
}

func (WaylandViewEvent) implementsViewEvent() {}
func (WaylandViewEvent) ImplementsEvent()     {}

func osMain() {
	select {}
}

type windowDriver func(*callbacks, []Option) error

// Instead of creating files with build tags for each combination of wayland +/- x11
// let each driver initialize these variables with their own versionInEntryFile of createWindow.
var wlDriver, x11Driver windowDriver

func newWindow(window *callbacks, options []Option) error {
	var errFirst error
	for _, d := range []windowDriver{wlDriver, x11Driver} {
		if d == nil {
			continue
		}
		err := d(window, options)
		if err == nil {
			go listenToSocketConn(window)
			return nil
		}
		if errFirst == nil {
			errFirst = err
		}
	}
	if errFirst != nil {
		return errFirst
	}
	return errors.New("app: no window driver available")
}

// xCursor contains mapping from pointer.Cursor to XCursor.
var xCursor = [...]string{
	pointer.CursorDefault:                  "left_ptr",
	pointer.CursorNone:                     "",
	pointer.CursorText:                     "xterm",
	pointer.CursorVerticalText:             "vertical-text",
	pointer.CursorPointer:                  "hand2",
	pointer.CursorCrosshair:                "crosshair",
	pointer.CursorAllScroll:                "fleur",
	pointer.CursorColResize:                "sb_h_double_arrow",
	pointer.CursorRowResize:                "sb_v_double_arrow",
	pointer.CursorGrab:                     "hand1",
	pointer.CursorGrabbing:                 "move",
	pointer.CursorNotAllowed:               "crossed_circle",
	pointer.CursorWait:                     "watch",
	pointer.CursorProgress:                 "left_ptr_watch",
	pointer.CursorNorthWestResize:          "top_left_corner",
	pointer.CursorNorthEastResize:          "top_right_corner",
	pointer.CursorSouthWestResize:          "bottom_left_corner",
	pointer.CursorSouthEastResize:          "bottom_right_corner",
	pointer.CursorNorthSouthResize:         "sb_v_double_arrow",
	pointer.CursorEastWestResize:           "sb_h_double_arrow",
	pointer.CursorWestResize:               "left_side",
	pointer.CursorEastResize:               "right_side",
	pointer.CursorNorthResize:              "top_side",
	pointer.CursorSouthResize:              "bottom_side",
	pointer.CursorNorthEastSouthWestResize: "fd_double_arrow",
	pointer.CursorNorthWestSouthEastResize: "bd_double_arrow",
}

/* Format of appName.desktop entry file
[Desktop Entry]
Version=Application's versionInEntryFile default is 1.0.0
Type=Application
Name=Your App Name
Exec=Path to executable/binary file %U
Icon=Path to iconPath
MimeType=comma separated schemes (mimeType)
StartupNotify=bool(currently always true)
Terminal=bool(currently always false)
SingleMainWindow=bool(currently always true)"
*/

/* Default paths used
~/.local/share
~/.local/share/applications
~/.local/share/applications/entryFile.desktop
~/.local/share/appDataDir
~/.local/share/appDataDir/icons (app icons dir path)
~/.local/share/appDataDir/bin (app binaries dir path)
/tmp/socket
*/

// mimeType is comma separated schemes,this is the
// only value required for deep linking
// ex -ldflags="-X 'gioui.org/app.mimeType=x-scheme-handler/custom-uri'
var mimeType string

var socketConn net.Listener = nil

// socketDirPath Default is os.TempDir()
var socketDirPath string

// socketFileName is the unique socket connection filename.
// Using the same socketFileName ensures that only single instance of app is running
// Default is appBinaryName (suffix .sock is added if not present)
var socketFileName string

// desktopEntryDir
// Default is ~/.local/share/applications
var desktopEntryDir string

// name of the entry file
// Default is executable file with .desktop suffix
var entryFileName string

// appDataDir
// Default is ~/.local/share/appBinaryName
var appDataDir string

// binDirPath
// Default is appDataDir/bin
var binDirPath string

// appBinaryName
// Default is executable filename (filepath.Base(os.Args[0]))
var appBinaryName string

// iconsDirPath
// Default is appDataDir/icons
var iconsDirPath string

// versionInEntryFile for desktop entry file
// Default is to 1.0.0
var versionInEntryFile string

// nameInEntryFile for desktop entry file
// Default is appBinaryName
var nameInEntryFile string

// iconPath
// if provided, icon from iconPath is copied to iconsDirPath and
// new icon path is added to desktop entry file.
var iconPath string

func init() {
	if mimeType == "" {
		return
	}
	if appBinaryName == "" {
		appBinaryName = filepath.Base(os.Args[0])
	}
	if socketFileName == "" {
		socketFileName = appBinaryName
	}
	if socketDirPath == "" {
		socketDirPath = path.Join(os.TempDir())
	}
	var socketFile = path.Join(socketDirPath, socketFileName)
	if !strings.HasSuffix(socketFile, ".sock") {
		socketFile += ".sock"
	}
	c, err := net.Dial("unix", socketFile)
	if err != nil {
		// syscall.ECONNREFUSED error most likely indicates socket file exists but
		//  app instance is not running, hence we delete the socketFile
		if errors.Is(err, syscall.ECONNREFUSED) {
			// delete socket file
			_ = os.Remove(socketFile)
		}
		// we exit with error if error is other than these errors
		// (syscall.ENOENT indicates that socket file doesn't exist)
		if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
			log.Fatal(err)
		}
	} else {
		// since err is nil, we are certain that another instance of our app is running
		// if any arguments were passed to this app, then we pass it to already running
		// instance of our app
		if len(os.Args) > 1 {
			_, _ = c.Write([]byte(strings.Join(os.Args[1:], "\n")))
		}
		_ = c.Close()
		log.Fatal("another instance of app is already running")
	}
	socketConn, err = net.Listen("unix", socketFile)
	if err != nil {
		log.Fatal(err)
	}
	err = createDesktopEntry()
	if err != nil {
		_ = socketConn.Close()
		log.Fatal(err)
	}
}

// listenToSocketConn blocking function
func listenToSocketConn(window *callbacks) {
	if socketConn == nil {
		return
	}
	var socketFile = path.Join(socketDirPath, socketFileName)
	if !strings.HasSuffix(socketFile, ".sock") {
		socketFile += ".sock"
	}
	// Cleanup the sockfile.
	defer func() {
		_ = socketConn.Close()
		_ = os.Remove(socketFile)
	}()
	for {
		// Accept an incoming connection.
		conn, err := socketConn.Accept()
		if err != nil {
			continue
		}

		// Handle the connection in a separate goroutine.
		go func(conn net.Conn) {
			defer conn.Close()
			bs, err := io.ReadAll(conn)
			if err != nil {
				return
			}
			args := strings.Split(string(bs), "\n")
			for _, arg := range args {
				urlPtr, err := url.Parse(arg)
				if err != nil {
					return
				}
				window.Event(transfer.URLEvent{URL: urlPtr})
			}
		}(conn)
	}
}

// DesktopEntry file is only created when mimeType is provided
func createDesktopEntry() (err error) {
	if mimeType == "" {
		return nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dataHome := filepath.Join(homeDir, ".local", "share")
	if desktopEntryDir == "" {
		desktopEntryDir = filepath.Join(dataHome, "applications")
	}
	if appDataDir == "" {
		appDataDir = filepath.Join(dataHome, appBinaryName)
	}
	if socketFileName == "" {
		socketFileName = appBinaryName
	}
	if binDirPath == "" {
		binDirPath = filepath.Join(appDataDir, "bin")
	}
	if iconsDirPath == "" {
		iconsDirPath = filepath.Join(appDataDir, "icons")
	}
	if versionInEntryFile == "" {
		versionInEntryFile = "1.0.0"
	}
	if nameInEntryFile == "" {
		nameInEntryFile = appBinaryName
	}
	// Create applications directory if not exists.
	if _, err = os.Stat(desktopEntryDir); errors.Is(err, os.ErrNotExist) {
		err = os.MkdirAll(desktopEntryDir, os.ModePerm)
		if err != nil {
			return
		}
	}
	// Create bin directory if not exists
	if _, err = os.Stat(binDirPath); errors.Is(err, os.ErrNotExist) {
		err = os.MkdirAll(binDirPath, os.ModePerm)
		if err != nil {
			return
		}
	}
	// Create icons directory if not exists
	if _, err = os.Stat(iconsDirPath); errors.Is(err, os.ErrNotExist) {
		err = os.MkdirAll(iconsDirPath, os.ModePerm)
		if err != nil {
			return
		}
	}
	// copy iconPath from iconPath to iconPath path of entry file
	if iconPath != "" {
		var src, dst *os.File
		src, err = os.Open(iconPath)
		if err != nil {
			return err
		}
		defer src.Close()
		iconPath = filepath.Join(iconsDirPath, filepath.Base(iconPath))
		dst, err = os.Create(iconPath)
		if err != nil {
			return err
		}
		defer dst.Close()
		_, err = io.Copy(dst, src)
		if err != nil {
			return err
		}
	}
	binFilePath := filepath.Join(binDirPath, appBinaryName)
	// copy only if the src and dest binaries path are different
	if binFilePath != os.Args[0] {
		binFileBs, err := os.ReadFile(os.Args[0])
		if err != nil {
			return err
		}
		err = os.WriteFile(binFilePath, binFileBs, os.ModePerm)
		if err != nil {
			return err
		}
	}
	entryFile := fmt.Sprintf(
		"[Desktop Entry]\n"+
			"Version=%s\n"+
			"Type=Application\n"+
			"Name=%s\n"+
			"Exec=%s %%U\n"+
			"Icon=%s\n"+
			"MimeType=%s\n"+
			"StartupNotify=true\n"+
			"Terminal=false\n"+
			"SingleMainWindow=true",
		versionInEntryFile,
		nameInEntryFile,
		binFilePath,
		iconPath,
		mimeType,
	)
	if entryFileName == "" {
		entryFileName = appBinaryName
	}
	if !strings.HasSuffix(entryFileName, ".desktop") {
		entryFileName += ".desktop"
	}
	desktopEntryFilePath := filepath.Join(desktopEntryDir, entryFileName)
	err = os.WriteFile(desktopEntryFilePath, []byte(entryFile), os.ModePerm)
	if err != nil {
		return err
	}
	err = exec.Command("update-desktop-database", desktopEntryDir).Start()
	if err != nil {
		return
	}
	return nil
}
