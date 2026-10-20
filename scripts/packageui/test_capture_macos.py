"""Keep failed-start evidence without following project paths outside the fixture."""

from pathlib import Path
import os
import tempfile
import unittest

import capture_macos


@unittest.skipIf(os.name == "nt", "macOS trace fixtures use POSIX paths and links")
class CaptureTraceTests(unittest.TestCase):
    def test_retains_last_trace_after_runtime_removes_it(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            root = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE"
            trace = root / "project/_data/harbor/forj-dev.log"
            trace.parent.mkdir(parents=True)
            trace.write_text("frontend dependencies still starting\n")
            evidence = temporary / "evidence"
            capture_macos.retain_traces(temporary, evidence)
            trace.unlink()
            capture_macos.retain_traces(temporary, evidence)
            self.assertEqual((evidence / (root.name + "-live-launch.log")).read_text(), "frontend dependencies still starting\n")

    def test_rejects_linked_or_foreign_traces(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            foreign = temporary / "foreign"
            foreign.write_text("outside fixture")
            trace = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE/project/_data/harbor/forj-dev.log"
            trace.parent.mkdir(parents=True)
            trace.symlink_to(foreign)
            evidence = temporary / "evidence"
            capture_macos.retain_traces(temporary, evidence)
            self.assertFalse(evidence.exists())


if __name__ == "__main__":
    unittest.main()
