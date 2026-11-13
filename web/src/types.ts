export type Session = { username: string; csrf: string };
export type Repository = {
  name: string;
  description: string;
  public: boolean;
  revision: number;
  updatedAt: string;
  tags?: number;
};
export type Image = {
  tag?: string;
  digest: string;
  mediaType: string;
  size: number;
  platforms: string[];
  created?: string;
  references: { digest: string; size: number; mediaType: string }[];
  raw?: unknown;
};
export type Robot = {
  id: string;
  name: string;
  username: string;
  enabled: boolean;
  createdAt: string;
  expiresAt?: string;
  lastUsedAt?: string;
};
export type Overview = {
  ready: boolean;
  repositories: number;
  tags: number;
  public: number;
  robots: number;
  used: number;
  free: number;
  total: number;
  reserve: number;
  auditError: string;
  version: string;
};
export type List<T> = { items: T[]; total: number; page: number };
export type Detail = List<Image> & { repository: Repository };
export type Preview = {
  id: string;
  preview: {
    name: string;
    digest: string;
    tag: string;
    bytes: number;
    blobs: number;
    fingerprint: string;
  };
};
export type Audit = { time: string; actor: string; action: string; target: string; ip: string };
