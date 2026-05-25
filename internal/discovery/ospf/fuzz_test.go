package ospf

import "testing"

// FuzzParseNbrOID exercises the ospfNbrTable OID-suffix parser. The suffix
// after the table prefix has the form "<col>.<ip0>.<ip1>.<ip2>.<ip3>.<idx>".
// The parser must not panic on any oid + prefix combination.
func FuzzParseNbrOID(f *testing.F) {
	const validPrefix = ".1.3.6.1.2.1.14.10.1."
	f.Add(validPrefix+"3.10.0.0.1.0", validPrefix)              // valid: col=3, IP=10.0.0.1, addrLessIdx=0
	f.Add(validPrefix+"5.192.168.1.1.42", validPrefix)          // valid: col=5, IP=192.168.1.1, idx=42
	f.Add("", validPrefix)                                      // empty oid
	f.Add(validPrefix, validPrefix)                             // oid == prefix
	f.Add(validPrefix+"notnum.0.0.0.0.0", validPrefix)          // non-numeric column
	f.Add(validPrefix+"3.10.0.0.1", validPrefix)                // missing addrLessIdx
	f.Add(validPrefix+"3.10.0.0.1.0.0", validPrefix)            // extra component
	f.Add(validPrefix+"3.10.0.0.256.0", validPrefix)            // IP octet > 255
	f.Add(validPrefix+"3.10.0.-1.1.0", validPrefix)             // negative IP octet
	f.Add("differentprefix.3.10.0.0.1.0", validPrefix)          // prefix mismatch
	f.Add(".", ".")                                             // degenerate
	f.Add(validPrefix+"3.10.0.0.1.notnum", validPrefix)         // non-numeric idx

	f.Fuzz(func(t *testing.T, oid, prefix string) {
		_, _, _ = parseNbrOID(oid, prefix)
	})
}
