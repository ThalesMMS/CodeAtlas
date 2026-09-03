export interface DomainEvent {
  type: "FeatureChanged";
  featureID: string;
  value: string;
}

export class FeatureAggregate {
  private readonly events: DomainEvent[] = [];

  constructor(
    readonly id: string,
    private value: string,
  ) {}

  apply(nextValue: string): void {
    if (nextValue === this.value) return;
    this.value = nextValue;
    this.recordEvent({ type: "FeatureChanged", featureID: this.id, value: nextValue });
  }

  pendingEvents(): DomainEvent[] {
    return [...this.events];
  }

  markEventsDelivered(delivered: DomainEvent[]): void {
    const deliveredEvents = new Set(delivered);
    for (let index = this.events.length - 1; index >= 0; index -= 1) {
      if (deliveredEvents.has(this.events[index])) this.events.splice(index, 1);
    }
  }

  private recordEvent(event: DomainEvent): void {
    this.events.push(event);
  }
}
