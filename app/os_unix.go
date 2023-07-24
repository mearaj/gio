// SPDX-License-Identifier: Unlicense OR MIT

//go:build (linux && !android) || freebsd || openbsd
// +build linux,!android freebsd openbsd

package app

import (
	"errors"
	"fmt"
	"gioui.org/io/transfer"
	"github.com/adrg/xdg"
	syscall "golang.org/x/sys/unix"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"unsafe"

	"gioui.org/io/pointer"
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
// let each driver initialize these variables with their own entryVersion of createWindow.
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
Version=Application's entryVersion default is 1.0.0
Type=Application
Name=Your App Name
Exec=Path to executable file %U
Icon=Path to icon
MimeType=comma separated schemes (mimeType)
StartupNotify=bool(currently always true)
Terminal=bool(currently always false)
*/

/* Default paths used
~/.local/share (xdg.DataHome)
~/.local/share/applications (xdg.DataHome/applications)
~/.local/share/applications/entryFile.desktop (xdg.DataHome/applications/entryFile.desktop)
~/.local/share/appDataDir (xdg.DataHome/appDataDir)
~/.local/share/appDataDir/icons (xdg.DataHome/appDataDir/icons)(app icons dir path)
~/.local/share/appDataDir/bin (xdg.DataHome/appDataDir/bin) (app binaries dir path)
/tmp/socketFileName
*/

// mimeType is comma separated schemes,this is the
// only value required for deep linking
// ex -ldflags="-X 'gioui.org/app.mimeType=x-scheme-handler/custom-uri'
var mimeType string

// socketFileName is the unique socket connection filename.
// Using the same socketFileName ensures that only single instance of app is running
// Default is executable file (filepath.Base(os.Args[0]))
var socketFileName string

// desktopEntryDirPath
// Default is ~/.local/share/applications.(xdg.DataHome/applications)
var desktopEntryDirPath string

// entryName of the entry file
// Default is executable file with .desktop suffix
var entryFileName string

// dataDirPath
// by default it's ~/.local/share/nameOfExecutableFile
// by default icons and bin directory resides in this path
var dataDirPath string

// binDirPath
// ex -ldflags="-X 'gioui.org/app.binDirPath=binDirPath'
// by default it's dataDirPath/bin
var binDirPath string

// iconsDirPath
// ex -ldflags="-X 'gioui.org/app.iconsDirPath=iconsDirPath'
// by default it's dataDirPath/icons
var iconsDirPath string

// entryVersion for desktop entry file, defaults to 1.0.0
var entryVersion string

// entryName for desktop entry file, defaults to entryName of executable
var entryName string

// icon
// if provided, icon is copied to iconsDirPath and path is added
// to desktop entry file.
var icon string

var socketConn net.Listener = nil

var socketPath string

func init() {
	if mimeType == "" {
		return
	}
	if socketFileName == "" {
		socketFileName = filepath.Base(os.Args[0])
	}
	socketPath = path.Join(os.TempDir(), socketFileName)
	c, err := net.Dial("unix", socketPath)
	if err != nil {
		// syscall.ECONNREFUSED error most likely indicates socket file exists but
		//  app instance is not running
		if errors.Is(err, syscall.ECONNREFUSED) {
			// delete socket file
			_ = os.Remove(socketPath)
		}
		// we exit with error if error is other than these errors
		// (syscall.ENOENT indicates that socket file doesn't exist)
		if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
			log.Fatal(err)
		}
	} else {
		// since err is nil, we are certain that another instance of our app is running
		// if any arguments were passed to this app then we pass it to already running
		// instance of our app
		if len(os.Args) > 1 {
			for _, arg := range os.Args[1:] {
				_, _ = c.Write([]byte(arg))
			}
		}
		_ = c.Close()
		log.Fatal("another instance of app is already running")
	}
	socketConn, err = net.Listen("unix", socketPath)
	if err != nil {
		log.Fatal(err)
	}
	err = createDesktopEntry()
	if err != nil {
		log.Fatal(err)
	}
}

// listenToSocketConn blocking function
func listenToSocketConn(window *callbacks) {
	if socketConn == nil {
		return
	}
	// Cleanup the sockfile.
	clCh := make(chan os.Signal, 1)
	signal.Notify(clCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-clCh
		_ = socketConn.Close()
		_ = os.Remove(socketPath)
		os.Exit(0)
	}()
	defer func() {
		_ = socketConn.Close()
		_ = os.Remove(socketPath)
	}()
	for {
		// Accept an incoming connection.
		conn, err := socketConn.Accept()
		if err != nil {
			return
		}

		// Handle the connection in a separate goroutine.
		go func(conn net.Conn) {
			defer conn.Close()
			bs, err := io.ReadAll(conn)
			if err != nil {
				return
			}
			urlPtr, err := url.Parse(string(bs))
			if err != nil {
				return
			}
			window.Event(transfer.URLEvent{URL: urlPtr})
		}(conn)
	}
}

// DesktopEntry file is only created when mimeType is provided
func createDesktopEntry() (err error) {
	if mimeType == "" {
		return nil
	}
	if desktopEntryDirPath == "" {
		desktopEntryDirPath = filepath.Join(xdg.DataHome, "applications")
	}
	if dataDirPath == "" {
		dataDirPath = filepath.Join(xdg.DataHome, filepath.Base(os.Args[0]))
	}
	if socketFileName == "" {
		socketFileName = filepath.Base(os.Args[0])
	}
	if binDirPath == "" {
		binDirPath = filepath.Join(dataDirPath, "bin")
	}
	if iconsDirPath == "" {
		iconsDirPath = filepath.Join(dataDirPath, "icons")
	}
	if entryVersion == "" {
		entryVersion = "1.0.0"
	}
	if entryName == "" {
		entryName = filepath.Base(os.Args[0])
	}
	// Create applications directory if not exists.
	if _, err = os.Stat(desktopEntryDirPath); errors.Is(err, os.ErrNotExist) {
		err = os.MkdirAll(desktopEntryDirPath, os.ModePerm)
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
	// copy icon from icon to icon path of entry file
	if icon != "" {
		var src, dst *os.File
		src, err = os.Open(icon)
		if err != nil {
			return err
		}
		defer src.Close()
		icon = filepath.Join(iconsDirPath, filepath.Base(icon))
		dst, err = os.Create(icon)
		if err != nil {
			return err
		}
		defer dst.Close()
		_, err = io.Copy(dst, src)
		if err != nil {
			return err
		}
	}
	binFilePath := filepath.Join(binDirPath, filepath.Base(os.Args[0]))
	//
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
			"Terminal=false\n",
		entryVersion,
		entryName,
		binFilePath,
		icon,
		mimeType,
	)
	if entryFileName == "" {
		entryFileName = filepath.Base(os.Args[0])
	}
	if !strings.HasSuffix(entryFile, ".desktop") {
		entryFileName += ".desktop"
	}

	desktopEntryFilePath := filepath.Join(desktopEntryDirPath, entryFileName)
	err = os.WriteFile(desktopEntryFilePath, []byte(entryFile), os.ModePerm)
	if err != nil {
		return err
	}
	err = exec.Command("update-desktop-database", desktopEntryDirPath).Start()
	if err != nil {
		return
	}
	return nil
}
