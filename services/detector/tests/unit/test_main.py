import io

from detector.main import run


def test_run_writes_greeting() -> None:
    out = io.StringIO()

    run(out)

    assert out.getvalue() == "detector: hello\n"
