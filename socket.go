package vz

/*
#cgo darwin CFLAGS: -x objective-c -fno-objc-arc
#cgo darwin LDFLAGS: -lobjc -framework Foundation -framework Virtualization
# include "virtualization.h"
*/
import "C"
import (
	"net"
	"os"
	"runtime"
	"runtime/cgo"
	"time"
	"unsafe"
)

// SocketDeviceConfiguration for a socket device configuration.
type SocketDeviceConfiguration interface {
	NSObject

	socketDeviceConfiguration()
}

type baseSocketDeviceConfiguration struct{}

func (*baseSocketDeviceConfiguration) socketDeviceConfiguration() {}

var _ SocketDeviceConfiguration = (*VirtioSocketDeviceConfiguration)(nil)

// VirtioSocketDeviceConfiguration is a configuration of the Virtio socket device.
//
// This configuration creates a Virtio socket device for the guest which communicates with the host through the Virtio interface.
// Only one Virtio socket device can be used per virtual machine.
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketdeviceconfiguration?language=objc
type VirtioSocketDeviceConfiguration struct {
	pointer

	*baseSocketDeviceConfiguration
}

// NewVirtioSocketDeviceConfiguration creates a new VirtioSocketDeviceConfiguration.
//
// This is only supported on macOS 11 and newer, ErrUnsupportedOSVersion will
// be returned on older versions.
func NewVirtioSocketDeviceConfiguration() (*VirtioSocketDeviceConfiguration, error) {
	if macosMajorVersionLessThan(11) {
		return nil, ErrUnsupportedOSVersion
	}

	config := newVirtioSocketDeviceConfiguration(C.newVZVirtioSocketDeviceConfiguration())

	runtime.SetFinalizer(config, func(self *VirtioSocketDeviceConfiguration) {
		self.Release()
	})
	return config, nil
}

func newVirtioSocketDeviceConfiguration(ptr unsafe.Pointer) *VirtioSocketDeviceConfiguration {
	return &VirtioSocketDeviceConfiguration{
		pointer: pointer{
			ptr: ptr,
		},
	}
}

// VirtioSocketDevice a device that manages port-based connections between the guest system and the host computer.
//
// Don’t create a VirtioSocketDevice struct directly. Instead, when you request a socket device in your configuration,
// the virtual machine creates it and you can get it via SocketDevices method.
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketdevice?language=objc
type VirtioSocketDevice struct {
	dispatchQueue unsafe.Pointer
	pointer
}

func newVirtioSocketDevice(ptr, dispatchQueue unsafe.Pointer) *VirtioSocketDevice {
	return &VirtioSocketDevice{
		dispatchQueue: dispatchQueue,
		pointer: pointer{
			ptr: ptr,
		},
	}
}

// SetSocketListenerForPort configures an object to monitor the specified port for new connections.
//
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketdevice/3656679-setsocketlistener?language=objc
func (v *VirtioSocketDevice) SetSocketListenerForPort(listener *VirtioSocketListener, port uint32) {
	C.VZVirtioSocketDevice_setSocketListenerForPort(v.Ptr(), v.dispatchQueue, listener.Ptr(), C.uint32_t(port))
}

// RemoveSocketListenerForPort removes the listener object from the specfied port.
//
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketdevice/3656678-removesocketlistenerforport?language=objc
func (v *VirtioSocketDevice) RemoveSocketListenerForPort(listener *VirtioSocketListener, port uint32) {
	C.VZVirtioSocketDevice_removeSocketListenerForPort(v.Ptr(), v.dispatchQueue, C.uint32_t(port))
}

//export connectionHandler
func connectionHandler(connPtr, errPtr, cgoHandlerPtr unsafe.Pointer) {
	cgoHandler := *(*cgo.Handle)(cgoHandlerPtr)
	handler := cgoHandler.Value().(func(*VirtioSocketConnection, error))
	defer cgoHandler.Delete()
	// see: startHandler
	if err := newNSError(errPtr); err != nil {
		handler(nil, err)
	} else {
		conn, err := newVirtioSocketConnection(connPtr)
		handler(conn, err)
	}
}

// ConnectToPort Initiates a connection to the specified port of the guest operating system.
//
// This method initiates the connection asynchronously, and executes the completion handler when the results are available.
// If the guest operating system doesn’t listen for connections to the specifed port, this method does nothing.
//
// For a successful connection, this method sets the sourcePort property of the resulting VZVirtioSocketConnection object to a random port number.
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketdevice/3656677-connecttoport?language=objc
func (v *VirtioSocketDevice) ConnectToPort(port uint32, fn func(conn *VirtioSocketConnection, err error)) {
	cgoHandler := cgo.NewHandle(fn)
	C.VZVirtioSocketDevice_connectToPort(v.Ptr(), v.dispatchQueue, C.uint32_t(port), unsafe.Pointer(&cgoHandler))
}

// VirtioSocketListener a struct that listens for port-based connection requests from the guest operating system.
//
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketlistener?language=objc
type VirtioSocketListener struct {
	pointer
}

var shouldAcceptNewConnectionHandlers = map[unsafe.Pointer]func(conn *VirtioSocketConnection, err error) bool{}

// NewVirtioSocketListener creates a new VirtioSocketListener with connection handler.
//
// The handler is executed asynchronously. Be sure to close the connection used in the handler by calling `conn.Close`.
// This is to prevent connection leaks.
//
// This is only supported on macOS 11 and newer, ErrUnsupportedOSVersion will
// be returned on older versions.
func NewVirtioSocketListener(handler func(conn *VirtioSocketConnection, err error)) (*VirtioSocketListener, error) {
	if macosMajorVersionLessThan(11) {
		return nil, ErrUnsupportedOSVersion
	}

	ptr := C.newVZVirtioSocketListener()
	listener := &VirtioSocketListener{
		pointer: pointer{
			ptr: ptr,
		},
	}

	shouldAcceptNewConnectionHandlers[ptr] = func(conn *VirtioSocketConnection, err error) bool {
		go handler(conn, err)
		return true // must be connected
	}

	return listener, nil
}

//export shouldAcceptNewConnectionHandler
func shouldAcceptNewConnectionHandler(listenerPtr, connPtr, devicePtr unsafe.Pointer) C.bool {
	_ = devicePtr // NOTO(codehex): Is this really required? How to use?

	// see: startHandler
	conn, err := newVirtioSocketConnection(connPtr)
	return (C.bool)(shouldAcceptNewConnectionHandlers[listenerPtr](conn, err))
}

// VirtioSocketConnection is a port-based connection between the guest operating system and the host computer.
//
// You don’t create connection objects directly. When the guest operating system initiates a connection, the virtual machine creates
// the connection object and passes it to the appropriate VirtioSocketListener struct, which forwards the object to its delegate.
//
// This is implemented net.Conn interface. This is generated from duplicated a file descriptor which is returned
// from virtualization.framework. macOS cannot connect directly to the Guest operating system using vsock. The　vsock
// connection must always be made via virtualization.framework. The diagram looks like this.
//
// ┌─────────┐                     ┌────────────────────────────┐               ┌────────────┐
// │  macOS  │<─── unix socket ───>│  virtualization.framework  │<─── vsock ───>│  Guest OS  │
// └─────────┘                     └────────────────────────────┘               └────────────┘
//
// You will notice that this is not vsock in using this library. However, all data this connection goes through to the vsock
// connection to which the Guest OS is connected.
//
// This struct does not have any pointers for objects of the Objective-C. Because the various values
// of the VZVirtioSocketConnection object handled by Objective-C are no longer needed after the conversion
// to the Go struct.
//
// see: https://developer.apple.com/documentation/virtualization/vzvirtiosocketconnection?language=objc
type VirtioSocketConnection struct {
	rawConn         net.Conn
	destinationPort uint32
	sourcePort      uint32
}

var _ net.Conn = (*VirtioSocketConnection)(nil)

func newVirtioSocketConnection(ptr unsafe.Pointer) (*VirtioSocketConnection, error) {
	vzVirtioSocketConnection := C.convertVZVirtioSocketConnection2Flat(ptr)
	file := os.NewFile((uintptr)(vzVirtioSocketConnection.fileDescriptor), "")
	defer file.Close()
	rawConn, err := net.FileConn(file)
	if err != nil {
		return nil, err
	}
	conn := &VirtioSocketConnection{
		rawConn:         rawConn,
		destinationPort: (uint32)(vzVirtioSocketConnection.destinationPort),
		sourcePort:      (uint32)(vzVirtioSocketConnection.sourcePort),
	}
	return conn, nil
}

// Read reads data from connection of the vsock protocol.
func (v *VirtioSocketConnection) Read(b []byte) (n int, err error) { return v.rawConn.Read(b) }

// Write writes data to the connection of the vsock protocol.
func (v *VirtioSocketConnection) Write(b []byte) (n int, err error) { return v.rawConn.Write(b) }

// Close will be called when caused something error in socket.
func (v *VirtioSocketConnection) Close() error {
	return v.rawConn.Close()
}

// LocalAddr returns the local network address.
func (v *VirtioSocketConnection) LocalAddr() net.Addr { return v.rawConn.LocalAddr() }

// RemoteAddr returns the remote network address.
func (v *VirtioSocketConnection) RemoteAddr() net.Addr { return v.rawConn.RemoteAddr() }

// SetDeadline sets the read and write deadlines associated
// with the connection. It is equivalent to calling both
// SetReadDeadline and SetWriteDeadline.
func (v *VirtioSocketConnection) SetDeadline(t time.Time) error { return v.rawConn.SetDeadline(t) }

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (v *VirtioSocketConnection) SetReadDeadline(t time.Time) error {
	return v.rawConn.SetReadDeadline(t)
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (v *VirtioSocketConnection) SetWriteDeadline(t time.Time) error {
	return v.rawConn.SetWriteDeadline(t)
}

// DestinationPort returns the destination port number of the connection.
func (v *VirtioSocketConnection) DestinationPort() uint32 {
	return v.destinationPort
}

// SourcePort returns the source port number of the connection.
func (v *VirtioSocketConnection) SourcePort() uint32 {
	return v.sourcePort
}
