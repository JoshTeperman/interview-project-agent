from tinykv.store import Store


def test_put_get(tmp_path):
    s = Store(str(tmp_path / "data.log"))
    s.put(b"a", b"1")
    assert s.get(b"a") == b"1"
