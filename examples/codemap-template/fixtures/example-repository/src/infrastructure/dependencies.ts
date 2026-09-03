import { DomainEvent, FeatureAggregate } from "../domain/feature-aggregate";

export const operationLog: string[] = [];

export const featureRepository = {
  savedIDs: [] as string[],

  async find(id: string): Promise<FeatureAggregate> {
    return new FeatureAggregate(id, "old-value");
  },

  async save(aggregate: FeatureAggregate): Promise<void> {
    operationLog.push("save");
    this.savedIDs.push(aggregate.id);
  },
};

export const unitOfWork = {
  commits: 0,

  async commit(): Promise<void> {
    operationLog.push("commit");
    this.commits += 1;
  },
};

export const eventPublisher = {
  publishedTypes: [] as string[],

  async publish(events: DomainEvent[]): Promise<void> {
    operationLog.push("publish");
    this.publishedTypes.push(...events.map((event) => event.type));
  },
};

export const outbox = {
  stagedTypes: [] as string[],
  deliveredTypes: [] as string[],

  async stage(events: DomainEvent[]): Promise<void> {
    operationLog.push("stage");
    this.stagedTypes.push(...events.map((event) => event.type));
  },

  async markDelivered(events: DomainEvent[]): Promise<void> {
    operationLog.push("mark-delivered");
    this.deliveredTypes.push(...events.map((event) => event.type));
  },
};
