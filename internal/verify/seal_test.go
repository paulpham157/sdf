package verify

import (
	"encoding/binary"
	"strconv"
	"testing"
)

// runBPF evaluates a seccomp program on one system call.
func runBPF(t *testing.T, prog []bpfIns, vars map[string]uint32, nr int, arch uint32, ip uint64, args ...uint64) uint32 {
	t.Helper()
	var data [64]byte
	binary.LittleEndian.PutUint32(data[0:], uint32(nr))
	binary.LittleEndian.PutUint32(data[4:], arch)
	binary.LittleEndian.PutUint64(data[8:], ip)
	for i, a := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], a)
	}
	k := func(s string) uint32 {
		if v, ok := vars[s]; ok {
			return v
		}
		n, err := strconv.ParseUint(s, 0, 32)
		if err != nil {
			t.Fatalf("k %q", s)
		}
		return uint32(n)
	}
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.code {
		case bpfLd:
			a = binary.LittleEndian.Uint32(data[k(in.k):])
		case bpfRet:
			return k(in.k)
		case bpfJeq, bpfJge, bpfJset:
			var c bool
			switch in.code {
			case bpfJeq:
				c = a == k(in.k)
			case bpfJge:
				c = a >= k(in.k)
			default:
				c = a&k(in.k) != 0
			}
			if c {
				pc += int(in.jt)
			} else {
				pc += int(in.jf)
			}
		default:
			t.Fatalf("op %#x", in.code)
		}
	}
	t.Fatal("fell off the program")
	return 0
}

func TestSealFilter(t *testing.T) {
	allow, eperm, enosys := uint32(0x7fff0000), uint32(0x00050001), uint32(0x00050026)
	const fd, stub = 65535, 0x4a1000
	vars := map[string]uint32{"T_fd": fd, "T_ipHi": 0, "T_ipLo": stub, "T_ipEnd": stub + 32}
	for _, arch := range []string{"amd64", "arm64"} {
		s := sysnumsByArch[arch]
		prog, err := sealFilter(arch)
		if err != nil {
			t.Fatal(err)
		}
		run := func(nr int, ip uint64, args ...uint64) uint32 {
			return runBPF(t, prog, vars, nr, s.arch, ip, args...)
		}
		other := uint64(0x401000)
		readNr := map[string]int{"amd64": 0, "arm64": 63}
		for name, c := range map[string]struct {
			got, want uint32
		}{
			"write from the stub":           {run(s.write, stub+24, fd, 0, 0), allow},
			"write from the stub's end":     {run(s.write, stub+32, fd, 0, 0), eperm},
			"write elsewhere":               {run(s.write, other, fd, 0, 0), eperm},
			"write with high bits":          {run(s.write, other, 1<<32|fd, 0, 0), eperm},
			"write from a far stub address": {run(s.write, 1<<32|stub+24, fd, 0, 0), eperm},
			"write another fd":              {run(s.write, other, 1, 0, 0), allow},
			"read the channel":              {run(readNr[arch], other, fd), eperm},
			"splice into it":                {run(s.splice, other, 5, 0, fd), eperm},
			"splice elsewhere":              {run(s.splice, other, 5, 0, 6), allow},
			"sendmsg":                       {run(s.sendmsg, other, 5), eperm},
			"sendmmsg":                      {run(s.sendmmsg, other, 5), eperm},
			"io_uring_setup":                {run(s.uringSetup, other), eperm},
			"bpf":                           {run(s.bpf, other), eperm},
			"pidfd_getfd":                   {run(s.pidfdGetfd, other), eperm},
			"ptrace":                        {run(s.ptrace, other), eperm},
			"kill a pid equal to the fd":    {run(s.exempt[0], other, fd, 9), allow},
			"clone3":                        {run(s.clone3, other), enosys},
			"clone CLONE_PARENT":            {run(s.clone, other, 0x8000|17), eperm},
			"clone":                         {run(s.clone, other, 17), allow},
			"prctl PR_SET_DUMPABLE":         {run(s.prctl, other, 4, 1), eperm},
			"prctl other":                   {run(s.prctl, other, 15), allow},
			"other ABI":                     {runBPF(t, prog, vars, s.write, 0x40000003, stub+24, fd), eperm},
			"x32":                           {run(s.write|0x40000000, stub+24, fd), eperm},
		} {
			if c.got != c.want {
				t.Errorf("%s %s: %#x, want %#x", arch, name, c.got, c.want)
			}
		}
	}
}
