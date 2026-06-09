# YANG modules

This directory vendors the IETF YANG modules used to emit RFC 8345 network
topology, plus our own augmentation module.

## Provenance

The IETF modules below were copied **verbatim** (no hand-edits) from the
authoritative [YangModels/yang](https://github.com/YangModels/yang) repository.
Upstream base URL:
`https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/`

| File | RFC | Source URL |
| --- | --- | --- |
| `ietf-yang-types@2013-07-15.yang` | [RFC 6991](https://www.rfc-editor.org/rfc/rfc6991) | https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/ietf-yang-types@2013-07-15.yang |
| `ietf-inet-types@2013-07-15.yang` | [RFC 6991](https://www.rfc-editor.org/rfc/rfc6991) | https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/ietf-inet-types@2013-07-15.yang |
| `ietf-routing-types@2017-12-04.yang` | [RFC 8294](https://www.rfc-editor.org/rfc/rfc8294) | https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/ietf-routing-types@2017-12-04.yang |
| `ietf-network@2018-02-26.yang` | [RFC 8345](https://www.rfc-editor.org/rfc/rfc8345) | https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/ietf-network@2018-02-26.yang |
| `ietf-network-topology@2018-02-26.yang` | [RFC 8345](https://www.rfc-editor.org/rfc/rfc8345) | https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/ietf-network-topology@2018-02-26.yang |
| `ietf-l3-unicast-topology@2018-02-26.yang` | [RFC 8346](https://www.rfc-editor.org/rfc/rfc8346) | https://raw.githubusercontent.com/YangModels/yang/main/standard/ietf/RFC/ietf-l3-unicast-topology@2018-02-26.yang |

## Local module

| File | Description |
| --- | --- |
| `ntx-topology@2026-06-09.yang` | Exporter-specific augmentations (issue #75): per-link discovery protocol, link kind, confidence, adjacency; per-node inventory. Augments `ietf-network` and `ietf-network-topology`. A consumer that understands only base RFC 8345 ignores these augmentations. |

## Smoke test

Load the full closure (every module, no instance doc) with
[`yanglint`](https://github.com/CESNET/libyang):

```sh
yanglint -p yang yang/*.yang
```

A clean exit (no errors) means every `import` resolves within this directory.
