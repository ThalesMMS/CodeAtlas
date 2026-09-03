import { FeatureCommand } from "./feature-command";
import {
  eventPublisher, outbox,
  featureRepository,
  unitOfWork,
} from "../infrastructure/dependencies";

export interface FeatureResult {
  id: string;
  status: "accepted" | "rejected";
  message?: string;
}

export const executeFeature = {
  async run(command: FeatureCommand): Promise<FeatureResult> {
    const aggregate = await featureRepository.find(command.id);
    aggregate.apply(command.value);
    const pendingEvents = aggregate.pendingEvents();

    await featureRepository.save(aggregate);
    await outbox.stage(pendingEvents);
    await unitOfWork.commit();
    await eventPublisher.publish(pendingEvents);
    await outbox.markDelivered(pendingEvents);
    aggregate.markEventsDelivered(pendingEvents);

    return { id: aggregate.id, status: "accepted" };
  },
};
