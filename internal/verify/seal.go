package verify

import (
	"fmt"
	"strings"
)

// The seal. The hook's channel to the verifier is a socket on a file
// descriptor of the test process, and the hook's HMAC key is in the test
// process's memory: code under test that finds both can send an
// authenticated report (Review's round-2 attack read the key from
// runtime/debug.WriteHeapDump and found the socket by scanning its file
// descriptors). The key is therefore not what keeps code under test from
// reporting; on amd64 and arm64 the seal is:
//
//   - the hook moves the channel to a high, odd descriptor (65535, or below
//     RLIMIT_NOFILE), close-on-exec, and writes reports only through a
//     small assembly function (sealStub) that issues write(2) itself;
//   - the seccomp filter refuses (EPERM) every system call whose first
//     argument is that descriptor, unless it is write(2) issued from the
//     stub's instructions (seccomp sees the caller's instruction pointer).
//     Code under test cannot call the stub without assembly, unsafe or
//     //go:linkname, which the tripwire flags. So it cannot write, read,
//     dup, fcntl, fstat or close the channel;
//   - the other ways to get a second descriptor for the socket or to write
//     to it indirectly are refused too: sendmsg and sendmmsg (SCM_RIGHTS
//     could pass it back to the process under a new number), io_uring
//     (registered files), bpf (sockmap), and splice or copy_file_range
//     into it (third argument). pidfd_getfd and ptrace were already
//     refused, and /proc/self/fd/N cannot open a socket.
//
// System calls whose first argument is a process id or an address are not
// checked, so a pid equal to the descriptor is not refused by accident.
// Exec'd children inherit the filter: a child program's own descriptor
// with that number is refused too, and sendmsg fails in children as well.

// sysnums are the system call numbers the filter uses, per GOARCH.
type sysnums struct {
	arch                                                             uint32
	write, prctl, seccomp, ptrace, pvmr, pvmw, pidfdGetfd, clone     int
	clone3, sendmsg, sendmmsg, uringSetup, uringEnter, uringRegister int
	bpf, splice, copyFileRange                                       int
	exempt                                                           []int // first argument is a pid or an address
}

var sysnumsByArch = map[string]sysnums{
	"amd64": {arch: 0xC000003E, write: 1, prctl: 157, seccomp: 317, ptrace: 101, pvmr: 310, pvmw: 311, pidfdGetfd: 438, clone: 56,
		clone3: 435, sendmsg: 46, sendmmsg: 307, uringSetup: 425, uringEnter: 426, uringRegister: 427, bpf: 321, splice: 275, copyFileRange: 326,
		// kill tkill tgkill wait4 getrandom prlimit64 pidfd_open setpgid getpgid getsid sched_setaffinity sched_getaffinity futex mmap munmap mprotect madvise
		exempt: []int{62, 200, 234, 61, 318, 302, 434, 109, 121, 124, 203, 204, 202, 9, 11, 10, 28}},
	"arm64": {arch: 0xC00000B7, write: 64, prctl: 167, seccomp: 277, ptrace: 117, pvmr: 270, pvmw: 271, pidfdGetfd: 438, clone: 220,
		clone3: 435, sendmsg: 211, sendmmsg: 269, uringSetup: 425, uringEnter: 426, uringRegister: 427, bpf: 280, splice: 76, copyFileRange: 285,
		exempt: []int{129, 130, 131, 260, 278, 261, 434, 154, 155, 156, 122, 123, 98, 222, 215, 226, 233}},
}

// bpfIns is one classic BPF instruction; k is a number or the name of a
// variable of the hook (the channel descriptor and the stub's address are
// known only in the test process).
type bpfIns struct {
	code   uint16
	jt, jf uint8
	k      string
}

// bpfOp is an instruction before assembly: jumps name labels ("" is the
// next instruction); an op with label set and code 0 is the label itself.
type bpfOp struct {
	label  string
	code   uint16
	k      string
	jt, jf string
}

const (
	bpfLd   = 0x20 // A = data[k] (32 bits)
	bpfJeq  = 0x15
	bpfJge  = 0x35
	bpfJset = 0x45
	bpfRet  = 0x06

	seccompAllow = "0x7fff0000"
	seccompEPERM = "0x00050001"
	seccompENOSY = "0x00050026"

	// seccomp_data offsets: nr, arch, instruction_pointer (lo, hi), args[i] lo.
	offNr, offArch, offIPLo, offIPHi, offArg0, offArg2 = "0", "4", "8", "12", "16", "32"
)

func assembleBPF(ops []bpfOp) ([]bpfIns, error) {
	at := map[string]int{}
	n := 0
	for _, o := range ops {
		if o.code == 0 && o.label != "" {
			if _, dup := at[o.label]; dup {
				return nil, fmt.Errorf("label %s twice", o.label)
			}
			at[o.label] = n
			continue
		}
		n++
	}
	var out []bpfIns
	for _, o := range ops {
		if o.code == 0 && o.label != "" {
			continue
		}
		pc := len(out)
		off := func(l string) (uint8, error) {
			if l == "" {
				return 0, nil
			}
			t, ok := at[l]
			if !ok {
				return 0, fmt.Errorf("no label %s", l)
			}
			d := t - pc - 1
			if d < 0 || d > 255 {
				return 0, fmt.Errorf("jump to %s out of range", l)
			}
			return uint8(d), nil
		}
		jt, err := off(o.jt)
		if err != nil {
			return nil, err
		}
		jf, err := off(o.jf)
		if err != nil {
			return nil, err
		}
		out = append(out, bpfIns{o.code, jt, jf, o.k})
	}
	return out, nil
}

// sealFilter is the hook's seccomp program for goarch, or nil where there
// is none. Its variables: T_fd (the channel), T_ipHi, T_ipLo, T_ipEnd (the
// stub's instructions, [lo, end) within one 4 GiB page).
func sealFilter(goarch string) ([]bpfIns, error) {
	s, ok := sysnumsByArch[goarch]
	if !ok {
		return nil, nil
	}
	n := func(i int) string { return fmt.Sprint(i) }
	ops := []bpfOp{
		{code: bpfLd, k: offArch},
		{code: bpfJeq, k: fmt.Sprintf("0x%X", s.arch), jf: "deny"},
		{code: bpfLd, k: offNr},
		{code: bpfJset, k: "0x40000000", jt: "deny"}, // x32
		{code: bpfJeq, k: n(s.write), jf: "nr"},
		{code: bpfLd, k: offIPHi},
		{code: bpfJeq, k: "T_ipHi", jf: "reload"},
		{code: bpfLd, k: offIPLo},
		{code: bpfJge, k: "T_ipLo", jf: "reload"},
		{code: bpfJge, k: "T_ipEnd", jt: "reload", jf: "allow"},
		{label: "reload"},
		{code: bpfLd, k: offNr},
		{label: "nr"},
	}
	for _, d := range []int{s.ptrace, s.pvmr, s.pvmw, s.pidfdGetfd, s.sendmsg, s.sendmmsg, s.uringSetup, s.uringEnter, s.uringRegister, s.bpf} {
		ops = append(ops, bpfOp{code: bpfJeq, k: n(d), jt: "deny"})
	}
	ops = append(ops,
		bpfOp{code: bpfJeq, k: n(s.clone3), jt: "enosys"},
		bpfOp{code: bpfJeq, k: n(s.clone), jt: "clone"},
		bpfOp{code: bpfJeq, k: n(s.prctl), jt: "prctl"},
	)
	for _, e := range s.exempt {
		ops = append(ops, bpfOp{code: bpfJeq, k: n(e), jt: "allow"})
	}
	ops = append(ops,
		bpfOp{code: bpfJeq, k: n(s.splice), jt: "arg2"},
		bpfOp{code: bpfJeq, k: n(s.copyFileRange), jt: "arg2", jf: "arg0"},
		bpfOp{label: "arg2"},
		bpfOp{code: bpfLd, k: offArg2},
		bpfOp{code: bpfJeq, k: "T_fd", jt: "deny"},
		bpfOp{label: "arg0"},
		bpfOp{code: bpfLd, k: offArg0},
		bpfOp{code: bpfJeq, k: "T_fd", jt: "deny", jf: "allow"},
		bpfOp{label: "clone"},
		bpfOp{code: bpfLd, k: offArg0},
		bpfOp{code: bpfJset, k: "0x8000", jt: "deny", jf: "allow"}, // CLONE_PARENT
		bpfOp{label: "prctl"},
		bpfOp{code: bpfLd, k: offArg0},
		bpfOp{code: bpfJeq, k: "4", jt: "deny"}, // PR_SET_DUMPABLE
		bpfOp{label: "allow"},
		bpfOp{code: bpfRet, k: seccompAllow},
		bpfOp{label: "deny"},
		bpfOp{code: bpfRet, k: seccompEPERM},
		bpfOp{label: "enosys"},
		bpfOp{code: bpfRet, k: seccompENOSY},
	)
	return assembleBPF(ops)
}

func renderBPF(prog []bpfIns) string {
	var b strings.Builder
	for _, i := range prog {
		fmt.Fprintf(&b, "\t\t\t{0x%02x, %d, %d, %s},\n", i.code, i.jt, i.jf, i.k)
	}
	return b.String()
}

// sealStub is the assembly the overlay adds to package testing on amd64 and
// arm64: T_rawwrite issues write(2) (no Go scheduler involvement: the
// channel is blocking and a report is short), T_rawwritePC is its address.
// The seccomp filter allows writes to the channel from [pc, pc+32) only.
func sealStub(goarch string) string {
	switch goarch {
	case "amd64":
		return `#include "textflag.h"

TEXT ·T_rawwrite(SB),NOSPLIT,$0-32
	MOVQ fd+0(FP), DI
	MOVQ p+8(FP), SI
	MOVQ n+16(FP), DX
	MOVQ $1, AX
	SYSCALL
	MOVQ AX, r+24(FP)
	RET

TEXT ·T_rawwritePC(SB),NOSPLIT,$0-8
	MOVQ $·T_rawwrite(SB), AX
	MOVQ AX, ret+0(FP)
	RET
`
	case "arm64":
		return `#include "textflag.h"

TEXT ·T_rawwrite(SB),NOSPLIT,$0-32
	MOVD fd+0(FP), R0
	MOVD p+8(FP), R1
	MOVD n+16(FP), R2
	MOVD $64, R8
	SVC
	MOVD R0, r+24(FP)
	RET

TEXT ·T_rawwritePC(SB),NOSPLIT,$0-8
	MOVD $·T_rawwrite(SB), R0
	MOVD R0, ret+0(FP)
	RET
`
	}
	return ""
}
