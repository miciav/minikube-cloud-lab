import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "course_vm", Path(__file__).resolve().parents[1] / "scripts/verify-course-vm.py")
course_vm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(course_vm)


class CourseVMAcceptanceTest(unittest.TestCase):
    def setUp(self):
        self.inventory = dict.fromkeys(("clusters", "instances", "groups", "volumes", "disks", "operations"), [])
        self.inventory["instance-autoscaling"] = {"state": "disabled", "remaining": []}

    def test_existing_resources_stop_live_exercises(self):
        for key in self.inventory:
            with self.subTest(key=key):
                inventory = dict(self.inventory)
                inventory[key] = {"state": "active"} if key == "instance-autoscaling" else [{"status": "RUNNING"}]
                commands = []
                with patch.object(course_vm, "api", side_effect=inventory.__getitem__), self.assertRaises(RuntimeError):
                    course_vm.verify(lambda _, cmd: commands.append(cmd))
                self.assertFalse(any("tests/instance-cloud-init.sh" in cmd for cmd in commands))

    def test_empty_vm_runs_both_images_and_ends_with_platform_shutdown_suite(self):
        commands = []
        with patch.object(course_vm, "api", side_effect=self.inventory.__getitem__):
            course_vm.verify(lambda _, cmd: commands.append(cmd))
        self.assertEqual([cmd[-1] for cmd in commands if "tests/instance-cloud-init.sh" in cmd],
                         ["ubuntu-26.04", "debian-12"])
        self.assertIn(["bash", "tests/managed-services.sh"], commands)
        self.assertLess(commands.index(["bash", "tests/monitoring.sh"]), commands.index(["bash", "tests/capstone.sh"]))
        self.assertIn("scripts/verify-lab.py", commands[-1])
        self.assertTrue(any("scripts/verify-lecture03.py" in cmd for cmd in commands))
