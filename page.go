package bbolt

import (
	"fmt"
	"os"
	"sort"
	"unsafe"
)

const pageHeaderSize = unsafe.Sizeof(page{})

const minKeysPerPage = 2

const branchPageElementSize = unsafe.Sizeof(branchPageElement{})
const leafPageElementSize = unsafe.Sizeof(leafPageElement{})

const (
	branchPageFlag   = 0x01
	leafPageFlag     = 0x02
	metaPageFlag     = 0x04
	freelistPageFlag = 0x10
)

const (
	bucketLeafFlag = 0x01
)

type pgid uint64

type page struct {
	id       pgid   // page ID，从0开始
	flags    uint16 // page type flag
	count    uint16 // 页面中存储的数据数量，仅在页面类型是branch以及leaf的时候起作用| update：对于freelist页面，此值表示freelist列表长度
	overflow uint32 // 当前页面如果还不够存放数据，就会有后续页面，这个字段表示后续页面的数量
}

// typ returns a human readable page type string used for debugging.
func (p *page) typ() string {
	if (p.flags & branchPageFlag) != 0 {
		return "branch"
	} else if (p.flags & leafPageFlag) != 0 {
		return "leaf"
	} else if (p.flags & metaPageFlag) != 0 {
		return "meta"
	} else if (p.flags & freelistPageFlag) != 0 {
		return "freelist"
	}
	return fmt.Sprintf("unknown<%02x>", p.flags)
}

// meta returns a pointer to the metadata section of the page.
func (p *page) meta() *meta {
	return (*meta)(unsafeAdd(unsafe.Pointer(p), unsafe.Sizeof(*p)))
}

// leafPageElement retrieves the leaf node by index
func (p *page) leafPageElement(index uint16) *leafPageElement {
	return (*leafPageElement)(unsafeIndex(unsafe.Pointer(p), unsafe.Sizeof(*p),
		leafPageElementSize, int(index)))
}

// leafPageElements retrieves a list of leaf nodes.
func (p *page) leafPageElements() []leafPageElement {
	if p.count == 0 {
		return nil
	}
	var elems []leafPageElement
	data := unsafeAdd(unsafe.Pointer(p), unsafe.Sizeof(*p))
	unsafeSlice(unsafe.Pointer(&elems), data, int(p.count))
	return elems
}

// branchPageElement retrieves the branch node by index
func (p *page) branchPageElement(index uint16) *branchPageElement {
	return (*branchPageElement)(unsafeIndex(unsafe.Pointer(p), unsafe.Sizeof(*p),
		unsafe.Sizeof(branchPageElement{}), int(index)))
}

// branchPageElements retrieves a list of branch nodes.
func (p *page) branchPageElements() []branchPageElement {
	if p.count == 0 {
		return nil
	}
	var elems []branchPageElement
	data := unsafeAdd(unsafe.Pointer(p), unsafe.Sizeof(*p))
	unsafeSlice(unsafe.Pointer(&elems), data, int(p.count))
	return elems
}

// dump writes n bytes of the page to STDERR as hex output.
func (p *page) hexdump(n int) {
	buf := unsafeByteSlice(unsafe.Pointer(p), 0, 0, n)
	fmt.Fprintf(os.Stderr, "%x\n", buf)
}

type pages []*page

func (s pages) Len() int           { return len(s) }
func (s pages) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s pages) Less(i, j int) bool { return s[i].id < s[j].id }

// branchPageElement represents a node on a branch page.
type branchPageElement struct {
	pos   uint32 // 存储键相对于当前页面数据部分的偏移量
	ksize uint32 // 键的大小
	pgid  pgid   // 页面ID
}

// key returns a byte slice of the node key.
func (n *branchPageElement) key() []byte {
	return unsafeByteSlice(unsafe.Pointer(n), 0, int(n.pos), int(n.pos)+int(n.ksize))
}

// leafPageElement represents a node on a leaf page.
type leafPageElement struct {
	flags uint32 // 标志位，为0的时候表示就是普通的叶子节点，而为1的时候表示是子bucket
	pos   uint32 // 存储键相对于当前页面数据部分的偏移量
	ksize uint32 // 键的大小
	vsize uint32 // 存储数据的大小
}

// key returns a byte slice of the node key.
func (n *leafPageElement) key() []byte {
	i := int(n.pos)
	j := i + int(n.ksize)
	return unsafeByteSlice(unsafe.Pointer(n), 0, i, j)
}

// value returns a byte slice of the node value.
func (n *leafPageElement) value() []byte {
	i := int(n.pos) + int(n.ksize)
	j := i + int(n.vsize)
	return unsafeByteSlice(unsafe.Pointer(n), 0, i, j)
}

// PageInfo represents human readable information about a page.
type PageInfo struct {
	ID            int
	Type          string
	Count         int
	OverflowCount int
}

type pgids []pgid

func (s pgids) Len() int           { return len(s) }
func (s pgids) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s pgids) Less(i, j int) bool { return s[i] < s[j] }

// merge returns the sorted union of a and b.
func (a pgids) merge(b pgids) pgids {
	// Return the opposite slice if one is nil.
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(pgids, len(a)+len(b))
	mergepgids(merged, a, b)
	return merged
}

// mergepgids copies the sorted union of a and b into dst.
// If dst is too small, it panics.
func mergepgids(dst, a, b pgids) {
	if len(dst) < len(a)+len(b) {
		panic(fmt.Errorf("mergepgids bad len %d < %d + %d", len(dst), len(a), len(b)))
	}
	// Copy in the opposite slice if one is nil.
	if len(a) == 0 {
		copy(dst, b)
		return
	}
	if len(b) == 0 {
		copy(dst, a)
		return
	}

	// Merged will hold all elements from both lists.
	merged := dst[:0]

	// Assign lead to the slice with a lower starting value, follow to the higher value.
	lead, follow := a, b
	if b[0] < a[0] {
		lead, follow = b, a
	}

	// Continue while there are elements in the lead.
	for len(lead) > 0 {
		// Merge largest prefix of lead that is ahead of follow[0].
		n := sort.Search(len(lead), func(i int) bool { return lead[i] > follow[0] })
		merged = append(merged, lead[:n]...)
		if n >= len(lead) {
			break
		}

		// Swap lead and follow.
		lead, follow = follow, lead[n:]
	}

	// Append what's left in follow.
	_ = append(merged, follow...)
}
