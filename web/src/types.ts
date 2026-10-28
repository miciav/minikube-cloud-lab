export type Cluster = {
  name: string;
  machineType: string;
  minMachines: number;
  maxMachines: number;
  status: string;
  createdAt: string;
};
export type MachineType = { name: string; cpus: number; memoryMiB: number };
export type Machine = {
  name: string;
  cluster: string;
  machineType: string;
  controlPlane: boolean;
  status: string;
  createdBy: string;
};
export type Volume = {
  id: string;
  capacity: string;
  status: string;
  cluster: string;
  claim: string;
  policy: string;
  enforced: boolean;
  static?: boolean;
};
export type Operation = {
  id: string;
  type: string;
  target: string;
  status: string;
  error?: string;
};
export type StorageCapacity = {
  total: string;
  allocated: string;
  available: string;
};
